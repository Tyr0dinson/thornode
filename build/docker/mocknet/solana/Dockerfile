# Use an official Solana release image as a parent image
FROM solanalabs/solana:v1.18.21

# Set the working directory
WORKDIR /mocknet

# Copy the current directory contents into the container at /mocknet
COPY . /mocknet

# Ensure the entrypoint script is executable
RUN chmod +x /mocknet/entrypoint.sh

# Expose the RPC and WebSocket ports
EXPOSE 8899
EXPOSE 8900