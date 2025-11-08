FROM golang:1.25 AS build

WORKDIR /app

COPY . .

RUN go mod download
RUN go build -o /nortverse-downloader .

FROM debian:bookworm-slim

COPY --from=build /nortverse-downloader /nortverse-downloader

EXPOSE 9101

CMD [ "/nortverse-downloader" ]
