#!/bin/bash
# Stop search automation processes
# Usage: ./scripts/stop.sh

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Stopping Search Automation ==="

# Kill by PID files
if [ -f "$PROJECT_DIR/.worker.pid" ]; then
    WORKER_PID=$(cat "$PROJECT_DIR/.worker.pid")
    if kill -0 $WORKER_PID 2>/dev/null; then
        kill $WORKER_PID
        echo "Killed Python worker (PID: $WORKER_PID)"
    else
        echo "Python worker (PID: $WORKER_PID) already stopped."
    fi
    rm "$PROJECT_DIR/.worker.pid"
fi

if [ -f "$PROJECT_DIR/.orchestrator.pid" ]; then
    GO_PID=$(cat "$PROJECT_DIR/.orchestrator.pid")
    if kill -0 $GO_PID 2>/dev/null; then
        kill $GO_PID
        echo "Killed Go orchestrator (PID: $GO_PID)"
    else
        echo "Go orchestrator (PID: $GO_PID) already stopped."
    fi
    rm "$PROJECT_DIR/.orchestrator.pid"
fi

# Also kill by name as fallback
pkill -f "worker/main.py" 2>/dev/null && echo "Killed any remaining worker processes."
pkill -f "google-automation" 2>/dev/null && echo "Killed any remaining Go processes."

echo "=== All stopped ==="
