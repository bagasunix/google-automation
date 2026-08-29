#!/bin/bash
# Run search automation: start Python worker + Go orchestrator
# Usage: ./scripts/run.sh

set -e

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WORKER_DIR="$PROJECT_DIR/worker"
VENV_DIR="$WORKER_DIR/.venv"
PYTHON="$VENV_DIR/bin/python"
GO_BIN="$HOME/go-sdk/go/bin"

echo "=== Search Automation Starting ==="
echo ""

# --- 1. Setup Python venv if not exists ---
if [ ! -d "$VENV_DIR" ]; then
    echo "[1/5] Creating Python venv..."
    python3 -m venv "$VENV_DIR"
    "$VENV_DIR/bin/pip" install --upgrade pip
    "$VENV_DIR/bin/pip" install -r "$WORKER_DIR/requirements.txt"
    echo "      Installing playwright chromium..."
    "$VENV_DIR/bin/playwright" install chromium
    echo "      Done."
else
    echo "[1/5] Python venv exists, skipping."
fi

# --- 2. Generate gRPC Python stubs if not exists ---
GRPC_STUB="$WORKER_DIR/task_pb2.py"
GRPC_STUB_GRPC="$WORKER_DIR/task_pb2_grpc.py"
if [ ! -f "$GRPC_STUB" ] || [ ! -f "$GRPC_STUB_GRPC" ]; then
    echo "[2/5] Generating gRPC Python stubs..."
    "$PYTHON" -m grpc_tools.protoc \
        -I "$PROJECT_DIR/internal/grpc/proto" \
        --python_out="$WORKER_DIR" \
        --grpc_python_out="$WORKER_DIR" \
        "$PROJECT_DIR/internal/grpc/proto/task.proto"
    echo "      Done."
else
    echo "[2/5] gRPC stubs exist, skipping."
fi

# --- 3. Start Python worker ---
echo "[3/5] Starting Python worker on localhost:50051..."
cd "$WORKER_DIR"
"$PYTHON" main.py &
WORKER_PID=$!
echo "      Worker PID: $WORKER_PID"

# --- 4. Wait for worker to be ready ---
echo "[4/5] Waiting for Python worker to be ready..."
sleep 3
if kill -0 $WORKER_PID 2>/dev/null; then
    echo "      Worker is running."
else
    echo "      ERROR: Worker failed to start."
    exit 1
fi

# Auto-detect Go path
if command -v go &>/dev/null; then
    GO_CMD="go"
elif [ -f "$HOME/go-sdk/go/bin/go" ]; then
    export PATH="$HOME/go-sdk/go/bin:$PATH"
    GO_CMD="go"
fi

# --- 5. Start Go orchestrator ---
echo "[5/5] Starting Go orchestrator..."
cd "$PROJECT_DIR"
if [ -f "$PROJECT_DIR/bin/orchestrator" ]; then
    "$PROJECT_DIR/bin/orchestrator" &
else
    $GO_CMD run cmd/main.go &
fi
GO_PID=$!
echo "      Go PID: $GO_PID"

echo ""
echo "=== Both processes running ==="
echo "Python Worker PID: $WORKER_PID"
echo "Go Orchestrator PID: $GO_PID"
echo ""
echo "Save PIDs..."
echo "$WORKER_PID" > "$PROJECT_DIR/.worker.pid"
echo "$GO_PID" > "$PROJECT_DIR/.orchestrator.pid"
echo ""
echo "To stop: ./scripts/stop.sh"
echo "To view logs: tail -f /tmp/search-automation-*.log"

# Wait for either to exit
wait -n $WORKER_PID $GO_PID 2>/dev/null
echo ""
echo "One process exited. Stopping all..."
kill $WORKER_PID $GO_PID 2>/dev/null
exit 0
