package languageserver

import (
	"os"

	"github.com/mattn/go-isatty"
	"github.com/onflow/cadence/languageserver/integration"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/spf13/cobra"

	"github.com/onflow/cadence/languageserver/server"
)

var Cmd = &cobra.Command{
	Use:   "language-server",
	Short: "Start the Cadence language server",
	Run: func(cmd *cobra.Command, args []string) {
		if isatty.IsTerminal(os.Stdout.Fd()) {
			print(
				"This program implements the Language Server Protocol for Cadence.\n" +
					"Please check the documentation on how to run it.\n" +
					"It does nothing in a terminal, it should be run with an editor/IDE.\n",
			)
			os.Exit(1)
		}

		languageServer := server.NewServer()

		_, err := integration.NewFlowIntegration(languageServer)
		if err != nil {
			panic(err)
		}

		stream := jsonrpc2.NewBufferedStream(
			server.StdinStdoutReadWriterCloser{},
			jsonrpc2.VSCodeObjectCodec{},
		)

		<-languageServer.Start(stream)
	},
}
