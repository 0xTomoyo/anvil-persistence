# syntax=docker/dockerfile:1

FROM golang:1.19-bullseye

SHELL ["/bin/bash", "-c"]

RUN apt update

RUN apt install -y curl git

RUN curl -L https://foundry.paradigm.xyz | bash

RUN /root/.foundry/bin/foundryup

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

RUN go build -o /anvil-persistence

EXPOSE 8545

CMD [ "/anvil-persistence" ]
