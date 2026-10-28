package rollout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const ns = "commerce"

var created = time.Date(2026, 10, 28, 13, 0, 0, 0, time.UTC)

func template(version, image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "payments", VersionLabel: version}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "payments", Image: image}}},
	}
}

// fixture is payments with revisions 1 (v2.13.2) and 2 (v2.14.0, current).
func fixture() []runtime.Object {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "payments", Namespace: ns, UID: types.UID("dep-uid"), ResourceVersion: "7",
			Annotations: map[string]string{RevisionAnnotation: "2"},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "payments"}},
			Template: template("v2.14.0", "ghcr.io/agentkourier/demo-payments:v2.14.0"),
		},
	}
	rs := func(rev, hash string, tmpl corev1.PodTemplateSpec, replicas int32, age time.Duration, extra map[string]string) *appsv1.ReplicaSet {
		tmpl = *tmpl.DeepCopy()
		tmpl.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = hash
		ann := map[string]string{RevisionAnnotation: rev}
		for k, v := range extra {
			ann[k] = v
		}
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "payments-" + hash, Namespace: ns, Labels: tmpl.Labels, Annotations: ann,
				CreationTimestamp: metav1.NewTime(created.Add(age)),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "Deployment", Name: "payments", UID: d.UID, Controller: new(true),
				}},
			},
			Spec:   appsv1.ReplicaSetSpec{Template: tmpl},
			Status: appsv1.ReplicaSetStatus{Replicas: replicas},
		}
	}
	// An unrelated ReplicaSet with matching labels but another owner must be ignored.
	stray := rs("9", "stray", template("v9", "other:v9"), 1, 0, nil)
	stray.OwnerReferences[0].UID = "someone-else"
	return []runtime.Object{
		d,
		rs("2", "bbb", template("v2.14.0", "ghcr.io/agentkourier/demo-payments:v2.14.0"), 2, time.Hour, nil),
		rs("1", "aaa", template("v2.13.2", "ghcr.io/agentkourier/demo-payments:v2.13.2"), 0, 0,
			map[string]string{"kubernetes.io/change-cause": "release v2.13.2", "deployment.kubernetes.io/desired-replicas": "2"}),
		stray,
	}
}

func newService(objs ...runtime.Object) (*Service, *fake.Clientset) {
	cs := fake.NewClientset(objs...)
	return New(cs, ns, []string{"payments"}), cs
}

func TestHistory(t *testing.T) {
	svc, _ := newService(fixture()...)
	h, err := svc.History(context.Background(), ns, "payments")
	if err != nil {
		t.Fatal(err)
	}
	want := []Revision{
		{Revision: 1, Image: "ghcr.io/agentkourier/demo-payments:v2.13.2", VersionLabel: "v2.13.2", Created: "2026-10-28T13:00:00Z", Replicas: 0},
		{Revision: 2, Image: "ghcr.io/agentkourier/demo-payments:v2.14.0", VersionLabel: "v2.14.0", Created: "2026-10-28T14:00:00Z", Replicas: 2, Current: true},
	}
	if len(h.Revisions) != len(want) {
		t.Fatalf("revisions = %+v, want %+v", h.Revisions, want)
	}
	for i := range want {
		if h.Revisions[i] != want[i] {
			t.Errorf("revision[%d] = %+v, want %+v", i, h.Revisions[i], want[i])
		}
	}
}

func TestHistoryAllowsAnyDeploymentInNamespace(t *testing.T) {
	svc, _ := newService(fixture()...)
	_, err := svc.History(context.Background(), ns, "checkout")
	if err == nil || errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error, not a refusal", err)
	}
	if _, err := svc.History(context.Background(), "kube-system", "coredns"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("other namespace: err = %v, want ErrNotAllowed", err)
	}
}

func TestRollback(t *testing.T) {
	svc, cs := newService(fixture()...)
	ctx := context.Background()
	res, err := svc.Rollback(ctx, ns, "payments", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.DryRun || res.FromRevision != 2 || res.ToRevision != 1 || res.NewRevision != 3 ||
		res.Image != "ghcr.io/agentkourier/demo-payments:v2.13.2" || res.VersionLabel != "v2.13.2" {
		t.Errorf("result = %+v", res)
	}
	d, err := cs.AppsV1().Deployments(ns).Get(ctx, "payments", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Spec.Template.Spec.Containers[0].Image; got != "ghcr.io/agentkourier/demo-payments:v2.13.2" {
		t.Errorf("image = %q", got)
	}
	if _, ok := d.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; ok {
		t.Error("pod-template-hash label was copied into the Deployment")
	}
	if d.Spec.Template.Labels[VersionLabel] != "v2.13.2" {
		t.Errorf("version label = %q", d.Spec.Template.Labels[VersionLabel])
	}
	if d.Annotations["kubernetes.io/change-cause"] != "release v2.13.2" {
		t.Error("change-cause annotation not copied from the ReplicaSet")
	}
	if d.Annotations[RevisionAnnotation] != "2" || d.Annotations["deployment.kubernetes.io/desired-replicas"] != "" {
		t.Errorf("controller annotations were copied: %v", d.Annotations)
	}
}

func TestRollbackDryRunChangesNothing(t *testing.T) {
	svc, cs := newService(fixture()...)
	res, err := svc.Rollback(context.Background(), ns, "payments", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || !res.Changed {
		t.Errorf("result = %+v", res)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Errorf("dry run made a %s call", a.GetVerb())
		}
	}
}

func TestRollbackToCurrentIsNoop(t *testing.T) {
	svc, cs := newService(fixture()...)
	res, err := svc.Rollback(context.Background(), ns, "payments", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Errorf("rollback to the current revision changed something: %+v", res)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "update" {
			t.Error("rollback to the current revision issued an update")
		}
	}
}

func TestRollbackRefusals(t *testing.T) {
	tests := []struct {
		name, ns, dep string
		rev           int64
		allowlist     bool
		want          string
	}{
		{"other namespace", "default", "payments", 1, true, "only namespace"},
		{"deployment not allowlisted", ns, "checkout", 1, true, "allowed only for payments"},
		{"missing revision", ns, "payments", 7, false, "no revision 7"},
		{"zero revision", ns, "payments", 0, false, "to_revision"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, cs := newService(fixture()...)
			_, err := svc.Rollback(context.Background(), tt.ns, tt.dep, tt.rev, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if tt.allowlist && !errors.Is(err, ErrNotAllowed) {
				t.Errorf("err = %v, want ErrNotAllowed", err)
			}
			if tt.allowlist && len(cs.Actions()) != 0 {
				t.Errorf("refused call still reached the API: %v", cs.Actions())
			}
		})
	}
}

func TestRollbackPausedDeployment(t *testing.T) {
	objs := fixture()
	objs[0].(*appsv1.Deployment).Spec.Paused = true
	svc, _ := newService(objs...)
	if _, err := svc.Rollback(context.Background(), ns, "payments", 1, false); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("err = %v, want paused", err)
	}
}

func TestRollbackUpdateConflict(t *testing.T) {
	svc, cs := newService(fixture()...)
	cs.PrependReactor("update", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the object has been modified")
	})
	if _, err := svc.Rollback(context.Background(), ns, "payments", 1, false); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("err = %v, want the update error", err)
	}
}

func TestImageTag(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/a/b:v1.2.3":                    "v1.2.3",
		"localhost:5000/a/b:v2":                 "v2",
		"localhost:5000/a/b":                    "latest",
		"nginx":                                 "latest",
		"repo/img:v1@sha256:abc":                "v1",
		"repo/img@sha256:abc":                   "sha256:abc",
		"registry.local:5000/team/img@sha256:d": "sha256:d",
	} {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}
