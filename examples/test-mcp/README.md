# Test MCP server

This stdio server is a local integration fixture for the MCP adapter. It offers `read_file`, `search`, and side-effecting `create_issue`. File operations are scoped to `KERNEL_MCP_TEST_ROOT`; the fixture rejects absolute paths and symlinks that resolve outside the root. It is not intended as a production MCP server.

The adapter integration test launches the process itself:

```sh
go test -count=1 ./kernel/mcpclient
```

For a manual Kernel experiment, build the server and configure:

```sh
go build -o /tmp/temporality-test-mcp ./examples/test-mcp
export KERNEL_MCP_TEST_ROOT="$PWD/examples/code-change"
export KERNEL_MCP_COMMAND=/tmp/temporality-test-mcp
export KERNEL_MCP_ARGS='[]'
export KERNEL_MCP_ALLOW=read_file,search,create_issue
export KERNEL_MCP_APPROVAL=create_issue
```

The Kernel exposes only allowlisted tools. The side-effecting tool is gated by the Kernel's human approval workflow. The fixture writes created issues to `.test-issues.log` under its configured root.
