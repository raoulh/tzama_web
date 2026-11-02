FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deployd .

# static : pas de libc ni de shell ; nonroot est remplacé par l'uid de tzama dans compose.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/deployd /deployd
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/deployd", "healthcheck"]
ENTRYPOINT ["/deployd"]
CMD ["serve"]
