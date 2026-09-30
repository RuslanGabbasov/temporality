#!/bin/bash
# Temporality repository cleanup
# Run from repo root: bash scripts/cleanup-repo.sh

set -e

echo "=== Cleaning up Temporality repository ==="

# 1. Empty/artifact directories
echo "Removing empty directories..."
rm -rf aml/llm aml/migrations
rmdir aml 2>/dev/null || true
rm -rf frp
rm -rf .delta
rm -rf .mimocode
rm -rf bin

# 2. Old experiment scripts
echo "Removing old experiment scripts..."
rm -rf scripts/exp6 scripts/exp7 scripts/exp8

# 3. Old examples
echo "Removing old experiment examples..."
rm -rf examples/experiment4 examples/experiment5 examples/experiment6
rm -rf examples/experiment7 examples/experiment8 examples/experiment9

# 4. Old documentation
echo "Removing old documentation..."
rm -rf docs/history
rm -f docs/experiment4-rejected-path.md
rm -f docs/experiment5-long-horizon.md
rm -f docs/experiment6-competing-experiences.md
rm -f docs/experiment7-reinforcement-and-scale.md
rm -f docs/experiment8-long-horizon-evolution.md
rm -f docs/phase2-status-report.md
rm -f docs/temporality-roadmap-2026-09-26.md
rm -f docs/temporality-roadmap-2026-09-27.md
rm -f docs/temporality-roadmap-2026-09-28.md
rm -f docs/oss-research.md
rm -f docs/validation-report.md
rm -f docs/benchmarks/agent-kernel-live-integration-2026-09-24.md
rmdir docs/benchmarks 2>/dev/null || true

# 5. Sandbox workspaces (keep projects/ and lighthouse/)
echo "Removing old sandbox workspaces..."
cd .sandbox
rm -rf ws-* ws--* smoke-* fault-drill
rm -rf calculator forge gatekeeper nightlybox relay
cd ..

# 6. DS_Store files
echo "Removing .DS_Store files..."
find . -name ".DS_Store" -delete 2>/dev/null || true

# 7. Clean empty directories
echo "Removing empty directories..."
find . -type d -empty -delete 2>/dev/null || true

echo ""
echo "=== Cleanup complete ==="
echo ""
echo "Kept:"
echo "  - docs/DESIGN.md, docs/ROADMAP.md (current)"
echo "  - docs/decisions/ (architecture decisions)"
echo "  - docs/runbook.md, docs/product-architecture.md"
echo "  - docs/approval-model.md, docs/architecture.md, etc."
echo "  - examples/test-mcp/, examples/lead_coder_reviewer_qa/"
echo "  - scripts/cleanup.sh, scripts/list-*.sh"
echo "  - .sandbox/projects/, .sandbox/lighthouse/"