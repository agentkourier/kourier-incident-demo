# The incident demo on agent-kourier's kind full tier (context kind-agent-kourier-full). See README.md.
KUBE_CONTEXT ?= kind-agent-kourier-full
AGENT_KOURIER_CHECKOUT ?= ../agent-courier
K := kubectl --context $(KUBE_CONTEXT)
export KUBE_CONTEXT AGENT_KOURIER_CHECKOUT

.PHONY: help up observability tools agents commerce kourier down reset rollout-bad rollout-good heal status images
help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

up: observability tools agents commerce kourier ## Install everything (the tier must be up)

observability: ## Prometheus, Tempo, collector, Grafana, the alert and the Alertmanager route
	observability/install.sh

images: ## Build the MCP and loadgen images and load them into kind (service images: build them in demo-payments/demo-checkout)
	$(MAKE) -C mcp image
	docker build -q -t demo-loadgen:dev loadgen/ >/dev/null
	for i in demo-mcp:dev demo-loadgen:dev demo-payments:v2.13.0 demo-payments:v2.14.0 demo-checkout:v1.8.0; do kind load docker-image $$i --name agent-kourier-full; done

tools: ## The MCP servers (needs Secrets demo-slack-ids, demo-pager-slack, demo-ic-slack, demo-grafana-token in demo-tools)
	$(K) apply -k mcp/deploy

agents: ## The three kagent agents and their prompts
	$(K) apply -f agents/prompts.yaml -f agents/agents.yaml
	sha=$$(shasum agents/prompts.yaml | cut -c1-12); for a in checkout-sre incident-commander payments-sre; do \
	  $(K) -n kagent annotate agent $$a demo.agentkourier.dev/prompts-sha=$$sha --overwrite >/dev/null; done
	$(K) -n kagent wait --for=condition=Ready agent/checkout-sre agent/incident-commander agent/payments-sre --timeout=300s

commerce: ## payments v2.13.0, checkout and loadgen
	scripts/reset.sh

kourier: ## The three Agent Kourier releases
	deploy/kourier/install.sh

down: ## Remove the releases, agents, tools and services (observability stays)
	deploy/kourier/down.sh
	-$(K) delete -f agents/agents.yaml -f agents/prompts.yaml --ignore-not-found
	-$(K) delete -k mcp/deploy --ignore-not-found
	-$(K) delete -k deploy/commerce --ignore-not-found
	-$(K) delete -f loadgen/deploy/loadgen.yaml --ignore-not-found

reset: ## Back to the starting state between takes
	scripts/reset.sh

rollout-bad: ## Ship payments v2.14.0 (the breaking change)
	scripts/rollout.sh v2.14.0

rollout-good: ## Deploy payments v2.13.0 forward (the agent's rollback is the demo's way back)
	scripts/rollout.sh v2.13.0

heal: ## After a Docker or node restart: make the kagent worker register again
	scripts/heal.sh

status: ## Pods, agents and the alert
	$(K) -n commerce get pods
	$(K) -n agent-kourier get pods
	$(K) -n kagent get agents checkout-sre incident-commander payments-sre
