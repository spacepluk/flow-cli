package evm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/filters"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/onflow/cadence"
	"github.com/onflow/cadence/encoding/ccf"
	"github.com/onflow/flow-go-sdk"
	"github.com/onflow/flow-go/fvm/evm/emulator"
	evmTypes "github.com/onflow/flow-go/fvm/evm/types"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/onflow/flow-cli/flowkit"
	"github.com/onflow/flow-cli/flowkit/output"
	"github.com/onflow/flow-cli/internal/command"
)

//go:embed service.abi
var serviceABI []byte

type flagsRPC struct{}

var rpcFlags = flagsRPC{}

var rpcCommand = &command.Command{
	Cmd: &cobra.Command{
		Use:     "rpc",
		Short:   "Start the RPC ethereum server",
		Args:    cobra.ExactArgs(0),
		Example: "flow rpc",
	},
	Flags: &rpcFlags,
	RunS:  rpcRun,
}

// todo only for demo, super hacky now

func rpcRun(
	args []string,
	_ command.GlobalFlags,
	_ output.Logger,
	flow flowkit.Services,
	state *flowkit.State,
) (command.Result, error) {
	logger := zerolog.New(os.Stdout).With().Str("module", "grpc").Logger()
	api := &ethAPI{flow: flow, log: logger, state: state, nonces: make(map[common.Address]uint64)}

	server := rpc.NewServer()
	err := server.RegisterName("eth", api)
	if err != nil {
		return nil, err
	}
	err = server.RegisterName("net", &NetAPI{})
	if err != nil {
		return nil, err
	}

	handler := server.WebsocketHandler([]string{"*"})
	//handler := node.NewWSHandlerStack(server, nil)
	go http.ListenAndServe(":8001", handler)

	mux := http.NewServeMux()
	mux.Handle("/", requestLogger(logger, server))
	err = http.ListenAndServe(":9000", mux)
	if err != nil {
		return nil, err
	}

	server.Stop()
	return nil, nil
}

func requestLogger(logger zerolog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(body)) // recreate the body for next handler
		}
		logger.Info().Str("method", r.Method).Str("body", string(body)).Msg("request")
		next.ServeHTTP(w, r)
	})
}

func callServiceMethod(flow flowkit.Services, method string, args ...interface{}) ([]byte, error) {
	const serviceContract = "e536720791a7dadbebdbcd8c8546fb0791a11901"

	ABI, err := abi.JSON(bytes.NewReader(serviceABI))
	if err != nil {
		return nil, fmt.Errorf("can't deserialize ABI file: %w", err)
	}

	data, err := ABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("can't prepare arguments: %w", err)
	}

	val, err := executeCall(flow, serviceContract, "f8d6e0586b0a20c7", data)
	if err != nil {
		return nil, err
	}

	return val, err
}

type ethAPI struct {
	flow   flowkit.Services
	state  *flowkit.State
	log    zerolog.Logger
	nonces map[common.Address]uint64
}

func (e *ethAPI) Call(
	ctx context.Context,
	args TransactionArgs,
	blockNumberOrHash *rpc.BlockNumberOrHash,
	overrides *StateOverride,
	blockOverrides *BlockOverrides,
) (hexutil.Bytes, error) {
	e.log.Info().Str("to", args.To.String()).Str("data", args.Data.String()).Msg("call")

	val, err := executeCall(
		e.flow,
		strings.ReplaceAll(args.To.String(), "0x", ""),
		"f8d6e0586b0a20c7", // todo set from args
		*args.Data,
	)

	return val, err
}

func (e *ethAPI) SendRawTransaction(
	ctx context.Context,
	input hexutil.Bytes,
) (common.Hash, error) {
	e.log.Info().Str("data", input.String()).Msg("send raw transaction")

	tx := types.Transaction{}
	txStream := rlp.NewStream(bytes.NewReader(input), uint64(len(input)))
	err := tx.DecodeRLP(txStream)
	if err != nil {
		e.log.Error().Err(err).Msg("")
		return common.Hash{}, err
	}

	sender, err := types.Sender(emulator.GetDefaultSigner(), &tx)
	if err != nil {
		e.log.Error().Err(err).Msg("")
		return common.Hash{}, err
	}

	// todo probably decode rlp the tx and then check the account and increase nonce counter if successful
	txRes, err := sendSignedTx(e.flow, e.state, input)
	if err != nil {
		e.log.Error().Err(err).Msg("")
		return common.Hash{}, err
	}

	e.parseEvents(txRes)
	e.nonces[sender]++
	e.log.Info().Str("account", sender.String()).Uint64("nonce", e.nonces[sender]).Msg("updating nonce")

	return tx.Hash(), nil
}

func (e *ethAPI) parseEvents(res *flow.TransactionResult) error {
	for _, ev := range res.Events {
		if ev.Type != string(evmTypes.EventTypeTransactionExecuted) {
			continue
		}

		val, err := ccf.Decode(nil, ev.Payload)
		if err != nil {
			return err
		}

		event, ok := val.(cadence.Event)
		if !ok {
			return fmt.Errorf("event of wrong type")
		}

		fmt.Println(event)
		/*for i, f := range event.GetFields() {
			if f.Identifier == "" {
				val := event.GetFieldValues()[i]

			}
		}*/
	}

	return nil
}

func (e *ethAPI) Ping() (int, error) {
	return 1, nil
}

func (e *ethAPI) GetTransactionCount(
	ctx context.Context,
	address common.Address,
	blockNumberOrHash rpc.BlockNumberOrHash,
) (*hexutil.Uint64, error) {
	// todo maybe add internal counter
	var nonce hexutil.Uint64
	stored := e.nonces[address]
	nonce = (hexutil.Uint64)(stored)

	e.log.Info().Uint64("nonce", stored).Msg("get transaction count")
	return &nonce, nil
}

func (e *ethAPI) BlockNumber() hexutil.Uint64 {

	val, err := callServiceMethod(e.flow, "getBlock")
	if err != nil {
		e.log.Error().Err(err).Msg("")
		panic(err)
	}

	block := hexutil.Uint64(binary.BigEndian.Uint64(val[len(val)-8:]))

	e.log.Info().Str("number", block.String()).Msg("get latest block number")
	return block
}

// eth_getLogs
// GetLogs returns logs matching the given argument that are stored within the state.
func (e *ethAPI) GetLogs(
	ctx context.Context,
	criteria filters.FilterCriteria,
) ([]*types.Log, error) {
	return []*types.Log{}, nil
}

// eth_newFilter
// NewFilter creates a new filter and returns the filter id. It can be
// used to retrieve logs when the state changes. This method cannot be
// used to fetch logs that are already stored in the state.
//
// Default criteria for the from and to block are "latest".
// Using "latest" as block number will return logs for mined blocks.
// Using "pending" as block number returns logs for not yet mined (pending) blocks.
// In case logs are removed (chain reorg) previously returned logs are returned
// again but with the removed property set to true.
//
// In case "fromBlock" > "toBlock" an error is returned.
func (e *ethAPI) NewFilter(
	criteria filters.FilterCriteria,
) (rpc.ID, error) {
	return "", nil
}

// eth_uninstallFilter
// UninstallFilter removes the filter with the given filter id.
func (e *ethAPI) UninstallFilter(id rpc.ID) bool {
	return false
}

// eth_getFilterLogs
// GetFilterLogs returns the logs for the filter with the given id.
// If the filter could not be found an empty array of logs is returned.
func (e *ethAPI) GetFilterLogs(
	ctx context.Context,
	id rpc.ID,
) ([]*types.Log, error) {
	return []*types.Log{}, nil
}

// eth_getFilterChanges
// GetFilterChanges returns the logs for the filter with the given id since
// last time it was called. This can be used for polling.
//
// For pending transaction and block filters the result is []common.Hash.
// (pending)Log filters return []Log.
func (e *ethAPI) GetFilterChanges(id rpc.ID) (interface{}, error) {
	return []interface{}{}, nil
}

func (e *ethAPI) GetTransactionReceipt(
	ctx context.Context,
	hash common.Hash,
) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (e *ethAPI) ChainId() *hexutil.Big {
	e.log.Info().Msg("get chain id")
	return (*hexutil.Big)(big.NewInt(666)) // hardcode testnet
}

func (e *ethAPI) GetBlockByNumber(
	ctx context.Context,
	blockNumber rpc.BlockNumber,
	fullTx bool,
) (map[string]interface{}, error) {
	e.log.Info().Msg("get block by number")
	return map[string]interface{}{}, nil
}

func (e *ethAPI) GetBalance(
	ctx context.Context,
	address common.Address,
	blockNrOrHash rpc.BlockNumberOrHash,
) (*hexutil.Big, error) {
	e.log.Info().Str("address", address.String()).Msg("get balance")

	val, err := callServiceMethod(e.flow, "getBalance", address)
	if err != nil {
		e.log.Error().Err(err).Msg("")
		return nil, err
	}

	balance := binary.BigEndian.Uint64(val[len(val)-8:])

	return (*hexutil.Big)(big.NewInt(int64(balance))), nil
}

func (e *ethAPI) EstimateGas(
	ctx context.Context,
	args TransactionArgs,
	blockNumberOrHash *rpc.BlockNumberOrHash,
	overrides *StateOverride,
) (hexutil.Uint64, error) {
	return hexutil.Uint64(21000 * 10), nil
}

func (e *ethAPI) GasPrice(ctx context.Context) (*hexutil.Big, error) {
	e.log.Info().Msg("gas price")
	return (*hexutil.Big)(big.NewInt(100)), nil
}

type TransactionArgs struct {
	From                 *common.Address `json:"from"`
	To                   *common.Address `json:"to"`
	Gas                  *hexutil.Uint64 `json:"gas"`
	GasPrice             *hexutil.Big    `json:"gasPrice"`
	MaxFeePerGas         *hexutil.Big    `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *hexutil.Big    `json:"maxPriorityFeePerGas"`
	Value                *hexutil.Big    `json:"value"`
	Nonce                *hexutil.Uint64 `json:"nonce"`

	// We accept "data" and "input" for backwards-compatibility reasons.
	// "input" is the newer name and should be preferred by clients.
	// Issue detail: https://github.com/ethereum/go-ethereum/issues/15628
	Data  *hexutil.Bytes `json:"data"`
	Input *hexutil.Bytes `json:"input"`

	// Introduced by AccessListTxType transaction.
	AccessList *types.AccessList `json:"accessList,omitempty"`
	ChainID    *hexutil.Big      `json:"chainId,omitempty"`
}

// NetAPI offers network related RPC methods
type NetAPI struct {
	networkVersion uint64
}

// Listening returns an indication if the node is listening for network connections.
func (s *NetAPI) Listening() bool {
	return true // always listening
}

// PeerCount returns the number of connected peers
func (s *NetAPI) PeerCount() hexutil.Uint {
	return 1
}

// Version returns the current ethereum protocol version.
func (s *NetAPI) Version() string {
	return fmt.Sprintf("%d", 666)
}

type StateOverride map[common.Address]OverrideAccount

type BlockOverrides struct {
	Number      *hexutil.Big
	Difficulty  *hexutil.Big
	Time        *hexutil.Uint64
	GasLimit    *hexutil.Uint64
	Coinbase    *common.Address
	Random      *common.Hash
	BaseFee     *hexutil.Big
	BlobBaseFee *hexutil.Big
}

type OverrideAccount struct {
	Nonce     *hexutil.Uint64              `json:"nonce"`
	Code      *hexutil.Bytes               `json:"code"`
	Balance   **hexutil.Big                `json:"balance"`
	State     *map[common.Hash]common.Hash `json:"state"`
	StateDiff *map[common.Hash]common.Hash `json:"stateDiff"`
}
