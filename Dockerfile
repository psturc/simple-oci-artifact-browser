FROM golang:1.22-alpine AS builder

WORKDIR /app

# COPY go.mod go.sum ./
# RUN go mod download

COPY templates .
COPY Dockerfile .
COPY main.go .

RUN go build -o filebrowser main.go

RUN apk --no-cache add curl tar
RUN curl -s -L https://github.com/oras-project/oras/releases/download/v1.2.0/oras_1.2.0_linux_amd64.tar.gz | tar xvz


FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/filebrowser .
COPY --from=builder /app/oras /usr/local/bin
COPY templates ./templates
RUN mkdir files


EXPOSE 8080
CMD ["./filebrowser"]