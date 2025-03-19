# Use the official Go image as the base image
FROM golang:1.23.4

# Set the working directory inside the container
WORKDIR /app

# Copy the Go application source code into the container
COPY . .

# Install Go dependencies
RUN go mod tidy

# Build the Go application
RUN go build -o gameServer main.go

# Expose port 8080 for the server
EXPOSE 8081

# Command to run the Go application
CMD ["/app/gameServer"]
