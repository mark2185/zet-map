FROM golang:tip-alpine

WORKDIR /app
CMD ["go", "run", "main.go"]
