package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-lambda-go/lambda"
)

type Request struct {
	Name string `json:"name"`
}

type Response struct {
	Message string `json:"message"`
}

func HandleRequest(ctx context.Context, req Request) (Response, error) {
	name := req.Name
	if name == "" {
		name = "World"
	}
	return Response{Message: fmt.Sprintf("Hello, %s!", name)}, nil
}

func main() { lambda.Start(HandleRequest) }
