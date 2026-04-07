dev:
	air -c .air.toml start

verify-fork-core:
	bash scripts/verify-fork-core.sh

upstream-check:
	bash scripts/upstream-integration-check.sh
