#!/bin/bash
set -e

# 1. Update pipeline.yaml
sed -i '' 's/model: sonnet/model: medium/g' internal/config/pipeline.yaml

# 2. Update config_test.go string matches
sed -i '' 's/"sonnet"/"medium"/g' internal/config/config_test.go
sed -i '' 's/model: sonnet/model: medium/g' internal/config/config_test.go
sed -i '' 's/sonnet:/medium:/g' internal/config/config_test.go
sed -i '' 's/"claude-sonnet-4.6"/"claude-medium"/g' internal/config/config_test.go
sed -i '' 's/"claude-sonnet"/"claude-medium"/g' internal/config/config_test.go
sed -i '' 's/"claude-medium"/"claude-medium"/g' internal/config/config_test.go # just in case
sed -i '' 's/model: test-sonnet/model: test-medium/g' internal/config/config_test.go
sed -i '' 's/model_ref: sonnet/model_ref: medium/g' internal/config/config_test.go
# Fix the specific assertions in TestDefaultConfig that were expecting large planner
sed -i '' '/if cfg.Planner.Model != "medium"/,/}/ s/"medium"/"large"/g' internal/config/config_test.go

# 3. tokenlimit test
sed -i '' 's/"opus"/"large"/g' internal/tokenlimit/runner_test.go
