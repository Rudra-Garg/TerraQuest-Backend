FROM golang:1.24-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /app

COPY go.mod go.sum ./
# Download dependencies
RUN go mod download

# Copy the entire application source code
COPY . .

# Build the Go application
# - CGO_ENABLED=0: Build without CGo for a static binary (good for Alpine/Scratch)
# - -ldflags="-w -s": Optimize the binary size (-w removes DWARF symbols, -s removes symbol table)
# - -o /app/server: Specify the output binary path
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/server ./cmd/server/main.go

#---------------------------------------------------------------------
# Stage 2: Runtime Stage - Create the minimal final image
#---------------------------------------------------------------------
# Use a minimal Alpine base image
FROM alpine:3.21

# Install ca-certificates for HTTPS requests (if your app makes external calls)
# GORM might need this for SSL connections to the DB
RUN apk add --no-cache ca-certificates tzdata

# Create a non-root user and group for security
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Set the working directory for the non-root user
WORKDIR /home/appuser

# Copy the compiled binary from the builder stage
# Change ownership to the non-root user
COPY --from=builder --chown=appuser:appgroup /app/server /home/appuser/server

# Copy the Swagger documentation files needed by the handler at runtime
# The gin-swagger handler might serve these static files or reference swagger.json/yaml
COPY --from=builder --chown=appuser:appgroup /app/docs /home/appuser/docs

# Switch to the non-root user
USER appuser

# Expose the port the application listens on (Cloud Run uses $PORT, default 8080)
# Your current main.go listens on :8080, which matches the default.
EXPOSE 8080

# Command to run the application when the container starts
CMD ["./server"]