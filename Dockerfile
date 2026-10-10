# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Download modules in their own layer so code changes don't re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---- runtime stage ----
# distroless: no shell or package manager, runs as non-root. Tiny attack surface.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
