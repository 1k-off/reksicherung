# Operator Compose reference

This compose file shows the services Reksicherung expects operators to run:

- OpenBao for secret references
- SQL Server restore target
- Shared staging/backup volumes used by MCP processes

Run MCP binaries (`reksicherung-mcp-*`) against those volumes with environment
variables documented in each command's README. Airon connects to the MCP
servers; Reksicherung never creates or removes the compose services.
