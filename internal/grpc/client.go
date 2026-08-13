// Package grpc provides the gRPC client that sends tasks to the Python worker.
package grpc

import (
	"context"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "google-automation/internal/grpc/proto"
)

// Client wraps the gRPC connection to the Python worker.
type Client struct {
	conn   *grpc.ClientConn
	stub   pb.WorkerServiceClient
	timeout time.Duration
}

// NewClient dials the Python worker at the given address (host:port).
func NewClient(host string, port int, timeoutSec int) (*Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}

	log.Printf("[grpc-client] connected to worker at %s", addr)
	return &Client{
		conn:   conn,
		stub:   pb.NewWorkerServiceClient(conn),
		timeout: time.Duration(timeoutSec) * time.Second,
	}, nil
}

// ExecuteTask sends a task request to the Python worker and returns the response.
func (c *Client) ExecuteTask(req *pb.TaskRequest) (*pb.TaskResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	resp, err := c.stub.ExecuteTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("execute task %s: %w", req.TaskId, err)
	}
	return resp, nil
}

// Close tears down the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
