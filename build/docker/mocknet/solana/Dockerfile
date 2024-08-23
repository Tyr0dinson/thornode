# Use an official Solana release image as a parent image
FROM solanalabs/solana:v1.18.21

# Set the working directory
WORKDIR /mocknet

# Copy the current directory contents into the container at /mocknet
COPY . /mocknet

# Expose the RPC port
EXPOSE 8899
EXPOSE 8900

COPY airdrop.sh /mocknet/airdrop.sh
RUN chmod +x /mocknet/airdrop.sh

# Define default command
CMD ["/bin/bash", "-c", "solana-test-validator --no-bpf-jit --log - --limit-ledger-size --url http://localhost:8899"]
