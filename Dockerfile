FROM bitnami/kubectl:1.28.7 as kubectl
FROM golang:1.21

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN go build -o dizkv .

COPY --from=kubectl /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/

CMD ["/app/dizkv"]

