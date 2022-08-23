
all: builder runner

builder:
	docker build -t registry.gitlab.com/thorchain/thornode:builder-v2 -f Dockerfile.builder .

runner:
	docker build -t registry.gitlab.com/thorchain/thornode:runner-base-v1 -f Dockerfile.runner .

smoke:
	docker build -t registry.gitlab.com/thorchain/thornode:smoke-v1 -f Dockerfile.smoke .

push-builder:
	docker push registry.gitlab.com/thorchain/thornode:builder-v2

push-runner:
	docker push registry.gitlab.com/thorchain/thornode:runner-base-v1

push-smoke:
	docker push registry.gitlab.com/thorchain/thornode:smoke-v1
