package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/filecoin-project/lotus/chain/actors/builtin"

	"github.com/multiformats/go-multiaddr"

	"github.com/ipfs/go-cid"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/multiformats/go-multihash"
	"github.com/urfave/cli/v2"
	cbg "github.com/whyrusleeping/cbor-gen"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/exitcode"

	"github.com/filecoin-project/lotus/api"
	lapi "github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/apibstore"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/state"
	"github.com/filecoin-project/lotus/chain/stmgr"
	"github.com/filecoin-project/lotus/chain/types"
)

var stateCmd = &cli.Command{
	Name:  "state",
	Usage: "Interact with and query filestar chain state",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "tipset",
			Usage: "specify tipset to call method on (pass comma separated array of cids)",
		},
	},
	Subcommands: []*cli.Command{
		statePowerCmd,
		stateSectorsCmd,
		stateActiveSectorsCmd,
		stateListActorsCmd,
		stateListMinersCmd,
		stateCircSupplyCmd,
		stateSectorCmd,
		stateGetActorCmd,
		stateLookupIDCmd,
		stateReplayCmd,
		stateSectorSizeCmd,
		stateReadStateCmd,
		stateListMessagesCmd,
		stateComputeStateCmd,
		stateCallCmd,
		stateGetDealSetCmd,
		stateWaitMsgCmd,
		stateSearchMsgCmd,
		stateMinerInfo,
		stateMarketCmd,
		stateExecTraceCmd,
	},
}

var stateMinerInfo = &cli.Command{
	Name:      "miner-info",
	Usage:     "Retrieve miner information",
	ArgsUsage: "[minerAddress]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must specify miner to get information for")
		}

		addr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		mi, err := api.StateMinerInfo(ctx, addr, ts.Key())
		if err != nil {
			return err
		}

		fmt.Printf("Owner:\t%s\n", mi.Owner)
		fmt.Printf("Worker:\t%s\n", mi.Worker)
		for i, controlAddress := range mi.ControlAddresses {
			fmt.Printf("Control %d: \t%s\n", i, controlAddress)
		}
		fmt.Printf("PeerID:\t%s\n", mi.PeerId)
		fmt.Printf("SectorSize:\t%s (%d)\n", types.SizeStr(types.NewInt(uint64(mi.SectorSize))), mi.SectorSize)
		fmt.Printf("Multiaddrs: \t")
		for _, addr := range mi.Multiaddrs {
			a, err := multiaddr.NewMultiaddrBytes(addr)
			if err != nil {
				return xerrors.Errorf("undecodable listen address: %w", err)
			}
			fmt.Printf("%s ", a)
		}
		fmt.Println()

		cd, err := api.StateMinerProvingDeadline(ctx, addr, ts.Key())
		if err != nil {
			return xerrors.Errorf("getting miner info: %w", err)
		}

		fmt.Printf("Proving Period Start:\t%s\n", EpochTime(cd.CurrentEpoch, cd.PeriodStart))

		return nil
	},
}

func ParseTipSetString(ts string) ([]cid.Cid, error) {
	strs := strings.Split(ts, ",")

	var cids []cid.Cid
	for _, s := range strs {
		c, err := cid.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, err
		}
		cids = append(cids, c)
	}

	return cids, nil
}

func LoadTipSet(ctx context.Context, cctx *cli.Context, api api.FullNode) (*types.TipSet, error) {
	tss := cctx.String("tipset")
	if tss == "" {
		return nil, nil
	}

	return ParseTipSetRef(ctx, api, tss)
}

func ParseTipSetRef(ctx context.Context, api api.FullNode, tss string) (*types.TipSet, error) {
	if tss[0] == '@' {
		if tss == "@head" {
			return api.ChainHead(ctx)
		}

		var h uint64
		if _, err := fmt.Sscanf(tss, "@%d", &h); err != nil {
			return nil, xerrors.Errorf("parsing height tipset ref: %w", err)
		}

		return api.ChainGetTipSetByHeight(ctx, abi.ChainEpoch(h), types.EmptyTSK)
	}

	cids, err := ParseTipSetString(tss)
	if err != nil {
		return nil, err
	}

	if len(cids) == 0 {
		return nil, nil
	}

	k := types.NewTipSetKey(cids...)
	ts, err := api.ChainGetTipSet(ctx, k)
	if err != nil {
		return nil, err
	}

	return ts, nil
}

var statePowerCmd = &cli.Command{
	Name:      "power",
	Usage:     "Query network or miner power",
	ArgsUsage: "[<minerAddress> (optional)]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		var maddr address.Address
		if cctx.Args().Present() {
			maddr, err = address.NewFromString(cctx.Args().First())
			if err != nil {
				return err
			}
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		power, err := api.StateMinerPower(ctx, maddr, ts.Key())
		if err != nil {
			return err
		}

		tp := power.TotalPower
		if cctx.Args().Present() {
			mp := power.MinerPower
			percI := types.BigDiv(types.BigMul(mp.QualityAdjPower, types.NewInt(1000000)), tp.QualityAdjPower)
			fmt.Printf("%s(%s) / %s(%s) ~= %0.4f%%\n", mp.QualityAdjPower.String(), types.SizeStr(mp.QualityAdjPower), tp.QualityAdjPower.String(), types.SizeStr(tp.QualityAdjPower), float64(percI.Int64())/10000)
		} else {
			fmt.Printf("%s(%s)\n", tp.QualityAdjPower.String(), types.SizeStr(tp.QualityAdjPower))
		}

		return nil
	},
}

var stateSectorsCmd = &cli.Command{
	Name:      "sectors",
	Usage:     "Query the sector set of a miner",
	ArgsUsage: "[minerAddress]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must specify miner to list sectors for")
		}

		maddr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		sectors, err := api.StateMinerSectors(ctx, maddr, nil, ts.Key())
		if err != nil {
			return err
		}

		for _, s := range sectors {
			fmt.Printf("%d: %x\n", s.SectorNumber, s.SealedCID)
		}

		return nil
	},
}

var stateActiveSectorsCmd = &cli.Command{
	Name:      "active-sectors",
	Usage:     "Query the active sector set of a miner",
	ArgsUsage: "[minerAddress]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must specify miner to list sectors for")
		}

		maddr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		sectors, err := api.StateMinerActiveSectors(ctx, maddr, ts.Key())
		if err != nil {
			return err
		}

		for _, s := range sectors {
			fmt.Printf("%d: %x\n", s.SectorNumber, s.SealedCID)
		}

		return nil
	},
}

var stateExecTraceCmd = &cli.Command{
	Name:      "exec-trace",
	Usage:     "Get the execution trace of a given message",
	ArgsUsage: "<messageCid>",
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return ShowHelp(cctx, fmt.Errorf("must pass message cid"))
		}

		mcid, err := cid.Decode(cctx.Args().First())
		if err != nil {
			return fmt.Errorf("message cid was invalid: %s", err)
		}

		capi, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		msg, err := capi.ChainGetMessage(ctx, mcid)
		if err != nil {
			return err
		}

		lookup, err := capi.StateSearchMsg(ctx, mcid)
		if err != nil {
			return err
		}

		ts, err := capi.ChainGetTipSet(ctx, lookup.TipSet)
		if err != nil {
			return err
		}

		pts, err := capi.ChainGetTipSet(ctx, ts.Parents())
		if err != nil {
			return err
		}

		cso, err := capi.StateCompute(ctx, pts.Height(), nil, pts.Key())
		if err != nil {
			return err
		}

		var trace *api.InvocResult
		for _, t := range cso.Trace {
			if t.Msg.From == msg.From && t.Msg.Nonce == msg.Nonce {
				trace = t
				break
			}
		}
		if trace == nil {
			return fmt.Errorf("failed to find message in tipset trace output")
		}

		out, err := json.MarshalIndent(trace, "", "  ")
		if err != nil {
			return err
		}

		fmt.Println(string(out))
		return nil
	},
}

var stateReplayCmd = &cli.Command{
	Name:      "replay",
	Usage:     "Replay a particular message",
	ArgsUsage: "<messageCid>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "show-trace",
			Usage: "print out full execution trace for given message",
		},
		&cli.BoolFlag{
			Name:  "detailed-gas",
			Usage: "print out detailed gas costs for given message",
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 1 {
			fmt.Println("must provide cid of message to replay")
			return nil
		}

		mcid, err := cid.Decode(cctx.Args().First())
		if err != nil {
			return fmt.Errorf("message cid was invalid: %s", err)
		}

		fapi, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		res, err := fapi.StateReplay(ctx, types.EmptyTSK, mcid)
		if err != nil {
			return xerrors.Errorf("replay call failed: %w", err)
		}

		fmt.Println("Replay receipt:")
		fmt.Printf("Exit code: %d\n", res.MsgRct.ExitCode)
		fmt.Printf("Return: %x\n", res.MsgRct.Return)
		fmt.Printf("Gas Used: %d\n", res.MsgRct.GasUsed)

		if cctx.Bool("detailed-gas") {
			fmt.Printf("Base Fee Burn: %d\n", res.GasCost.BaseFeeBurn)
			fmt.Printf("Overestimaton Burn: %d\n", res.GasCost.OverEstimationBurn)
			fmt.Printf("Miner Penalty: %d\n", res.GasCost.MinerPenalty)
			fmt.Printf("Miner Tip: %d\n", res.GasCost.MinerTip)
			fmt.Printf("Refund: %d\n", res.GasCost.Refund)
		}
		fmt.Printf("Total Message Cost: %d\n", res.GasCost.TotalCost)

		if res.MsgRct.ExitCode != 0 {
			fmt.Printf("Error message: %q\n", res.Error)
		}

		if cctx.Bool("show-trace") {
			fmt.Printf("%s\t%s\t%s\t%d\t%x\t%d\t%x\n", res.Msg.From, res.Msg.To, res.Msg.Value, res.Msg.Method, res.Msg.Params, res.MsgRct.ExitCode, res.MsgRct.Return)
			printInternalExecutions("\t", res.ExecutionTrace.Subcalls)
		}

		return nil
	},
}

var stateGetDealSetCmd = &cli.Command{
	Name:      "get-deal",
	Usage:     "View on-chain deal info",
	ArgsUsage: "[dealId]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must specify deal ID")
		}

		dealid, err := strconv.ParseUint(cctx.Args().First(), 10, 64)
		if err != nil {
			return xerrors.Errorf("parsing deal ID: %w", err)
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		deal, err := api.StateMarketStorageDeal(ctx, abi.DealID(dealid), ts.Key())
		if err != nil {
			return err
		}

		data, err := json.MarshalIndent(deal, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))

		return nil
	},
}

var stateListMinersCmd = &cli.Command{
	Name:  "list-miners",
	Usage: "list all miners in the network",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "sort-by",
			Usage: "criteria to sort miners by (none, num-deals)",
		},
	},
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		miners, err := api.StateListMiners(ctx, ts.Key())
		if err != nil {
			return err
		}

		switch cctx.String("sort-by") {
		case "num-deals":
			ndm, err := getDealsCounts(ctx, api)
			if err != nil {
				return err
			}

			sort.Slice(miners, func(i, j int) bool {
				return ndm[miners[i]] > ndm[miners[j]]
			})

			for i := 0; i < 50 && i < len(miners); i++ {
				fmt.Printf("%s %d\n", miners[i], ndm[miners[i]])
			}
			return nil
		default:
			return fmt.Errorf("unrecognized sorting order")
		case "", "none":
		}

		for _, m := range miners {
			fmt.Println(m.String())
		}

		return nil
	},
}

func getDealsCounts(ctx context.Context, lapi api.FullNode) (map[address.Address]int, error) {
	allDeals, err := lapi.StateMarketDeals(ctx, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	out := make(map[address.Address]int)
	for _, d := range allDeals {
		if d.State.SectorStartEpoch != -1 {
			out[d.Proposal.Provider]++
		}
	}

	return out, nil
}

var stateListActorsCmd = &cli.Command{
	Name:  "list-actors",
	Usage: "list all actors in the network",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		actors, err := api.StateListActors(ctx, ts.Key())
		if err != nil {
			return err
		}

		for _, a := range actors {
			fmt.Println(a.String())
		}

		return nil
	},
}

var stateGetActorCmd = &cli.Command{
	Name:      "get-actor",
	Usage:     "Print actor information",
	ArgsUsage: "[actorrAddress]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must pass address of actor to get")
		}

		addr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		a, err := api.StateGetActor(ctx, addr, ts.Key())
		if err != nil {
			return err
		}

		strtype := builtin.ActorNameByCode(a.Code)

		fmt.Printf("Address:\t%s\n", addr)
		fmt.Printf("Balance:\t%s\n", types.FIL(a.Balance))
		fmt.Printf("Nonce:\t\t%d\n", a.Nonce)
		fmt.Printf("Code:\t\t%s (%s)\n", a.Code, strtype)
		fmt.Printf("Head:\t\t%s\n", a.Head)

		return nil
	},
}

var stateLookupIDCmd = &cli.Command{
	Name:      "lookup",
	Usage:     "Find corresponding ID address",
	ArgsUsage: "[address]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "reverse",
			Aliases: []string{"r"},
			Usage:   "Perform reverse lookup",
		},
	},
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must pass address of actor to get")
		}

		addr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		var a address.Address
		if !cctx.Bool("reverse") {
			a, err = api.StateLookupID(ctx, addr, ts.Key())
		} else {
			a, err = api.StateAccountKey(ctx, addr, ts.Key())
		}

		if err != nil {
			return err
		}

		fmt.Printf("%s\n", a)

		return nil
	},
}

var stateSectorSizeCmd = &cli.Command{
	Name:      "sector-size",
	Usage:     "Look up miners sector size",
	ArgsUsage: "[minerAddress]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must pass miner's address")
		}

		addr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		mi, err := api.StateMinerInfo(ctx, addr, ts.Key())
		if err != nil {
			return err
		}

		fmt.Printf("%s (%d)\n", types.SizeStr(types.NewInt(uint64(mi.SectorSize))), mi.SectorSize)
		return nil
	},
}

var stateReadStateCmd = &cli.Command{
	Name:      "read-state",
	Usage:     "View a json representation of an actors state",
	ArgsUsage: "[actorAddress]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if !cctx.Args().Present() {
			return fmt.Errorf("must pass address of actor to get")
		}

		addr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		as, err := api.StateReadState(ctx, addr, ts.Key())
		if err != nil {
			return err
		}

		data, err := json.MarshalIndent(as.State, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))

		return nil
	},
}

var stateListMessagesCmd = &cli.Command{
	Name:  "list-messages",
	Usage: "list messages on chain matching given criteria",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "to",
			Usage: "return messages to a given address",
		},
		&cli.StringFlag{
			Name:  "from",
			Usage: "return messages from a given address",
		},
		&cli.Uint64Flag{
			Name:  "toheight",
			Usage: "don't look before given block height",
		},
		&cli.BoolFlag{
			Name:  "cids",
			Usage: "print message CIDs instead of messages",
		},
	},
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		var toa, froma address.Address
		if tos := cctx.String("to"); tos != "" {
			a, err := address.NewFromString(tos)
			if err != nil {
				return fmt.Errorf("given 'to' address %q was invalid: %w", tos, err)
			}
			toa = a
		}

		if froms := cctx.String("from"); froms != "" {
			a, err := address.NewFromString(froms)
			if err != nil {
				return fmt.Errorf("given 'from' address %q was invalid: %w", froms, err)
			}
			froma = a
		}

		toh := abi.ChainEpoch(cctx.Uint64("toheight"))

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		if ts == nil {
			head, err := api.ChainHead(ctx)
			if err != nil {
				return err
			}
			ts = head
		}

		windowSize := abi.ChainEpoch(100)

		cur := ts
		for cur.Height() > toh {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			end := toh
			if cur.Height()-windowSize > end {
				end = cur.Height() - windowSize
			}

			msgs, err := api.StateListMessages(ctx, &lapi.MessageMatch{To: toa, From: froma}, cur.Key(), end)
			if err != nil {
				return err
			}

			for _, c := range msgs {
				if cctx.Bool("cids") {
					fmt.Println(c.String())
					continue
				}

				m, err := api.ChainGetMessage(ctx, c)
				if err != nil {
					return err
				}
				b, err := json.MarshalIndent(m, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
			}

			if end <= 0 {
				break
			}

			next, err := api.ChainGetTipSetByHeight(ctx, end-1, cur.Key())
			if err != nil {
				return err
			}

			cur = next
		}

		return nil
	},
}

var stateComputeStateCmd = &cli.Command{
	Name:  "compute-state",
	Usage: "Perform state computations",
	Flags: []cli.Flag{
		&cli.Uint64Flag{
			Name:  "vm-height",
			Usage: "set the height that the vm will see",
		},
		&cli.BoolFlag{
			Name:  "apply-mpool-messages",
			Usage: "apply messages from the mempool to the computed state",
		},
		&cli.BoolFlag{
			Name:  "show-trace",
			Usage: "print out full execution trace for given tipset",
		},
		&cli.BoolFlag{
			Name:  "html",
			Usage: "generate html report",
		},
		&cli.BoolFlag{
			Name:  "json",
			Usage: "generate json output",
		},
		&cli.StringFlag{
			Name:  "compute-state-output",
			Usage: "a json file containing pre-existing compute-state output, to generate html reports without rerunning state changes",
		},
	},
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		h := abi.ChainEpoch(cctx.Uint64("vm-height"))
		if ts == nil {
			head, err := api.ChainHead(ctx)
			if err != nil {
				return err
			}
			ts = head
		}
		if h == 0 {
			h = ts.Height()
		}

		var msgs []*types.Message
		if cctx.Bool("apply-mpool-messages") {
			pmsgs, err := api.MpoolSelect(ctx, ts.Key(), 1)
			if err != nil {
				return err
			}

			for _, sm := range pmsgs {
				msgs = append(msgs, &sm.Message)
			}
		}

		var stout *lapi.ComputeStateOutput
		if csofile := cctx.String("compute-state-output"); csofile != "" {
			data, err := ioutil.ReadFile(csofile)
			if err != nil {
				return err
			}

			var o lapi.ComputeStateOutput
			if err := json.Unmarshal(data, &o); err != nil {
				return err
			}

			stout = &o
		} else {
			o, err := api.StateCompute(ctx, h, msgs, ts.Key())
			if err != nil {
				return err
			}

			stout = o
		}

		if cctx.Bool("json") {
			out, err := json.Marshal(stout)
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		}

		if cctx.Bool("html") {
			st, err := state.LoadStateTree(cbor.NewCborStore(apibstore.NewAPIBlockstore(api)), stout.Root)
			if err != nil {
				return xerrors.Errorf("loading state tree: %w", err)
			}

			codeCache := map[address.Address]cid.Cid{}
			getCode := func(addr address.Address) (cid.Cid, error) {
				if c, found := codeCache[addr]; found {
					return c, nil
				}

				c, err := st.GetActor(addr)
				if err != nil {
					return cid.Cid{}, err
				}

				codeCache[addr] = c.Code
				return c.Code, nil
			}

			return ComputeStateHTMLTempl(os.Stdout, ts, stout, getCode)
		}

		fmt.Println("computed state cid: ", stout.Root)
		if cctx.Bool("show-trace") {
			for _, ir := range stout.Trace {
				fmt.Printf("%s\t%s\t%s\t%d\t%x\t%d\t%x\n", ir.Msg.From, ir.Msg.To, ir.Msg.Value, ir.Msg.Method, ir.Msg.Params, ir.MsgRct.ExitCode, ir.MsgRct.Return)
				printInternalExecutions("\t", ir.ExecutionTrace.Subcalls)
			}
		}
		return nil
	},
}

func printInternalExecutions(prefix string, trace []types.ExecutionTrace) {
	for _, im := range trace {
		fmt.Printf("%s%s\t%s\t%s\t%d\t%x\t%d\t%x\n", prefix, im.Msg.From, im.Msg.To, im.Msg.Value, im.Msg.Method, im.Msg.Params, im.MsgRct.ExitCode, im.MsgRct.Return)
		printInternalExecutions(prefix+"\t", im.Subcalls)
	}
}

var compStateTemplate = `
<html>
 <head>
  <meta charset="UTF-8">
  <style>
   html, body { font-family: monospace; }
   a:link, a:visited { color: #004; }
   pre { background: #ccc; }
   small { color: #444; }
   .call { color: #00a; }
   .params { background: #dfd; }
   .ret { background: #ddf; }
   .error { color: red; }
   .exit0 { color: green; }
   .exec {
    padding-left: 15px;
    border-left: 2.5px solid;
    margin-bottom: 45px;
   }
   .exec:hover {
    background: #eee;
   }
   .slow-true-false { color: #660; }
   .slow-true-true { color: #f80; }
   .deemp { color: #444; }
   table {
    font-size: 12px;
    border-collapse: collapse;
   }
   tr {
   	border-top: 1px solid black;
   	border-bottom: 1px solid black;
   }
   tr.sum { border-top: 2px solid black; }
   tr:first-child { border-top: none; }
   tr:last-child { border-bottom: none; }


   .ellipsis-content,
   .ellipsis-toggle input {
     display: none;
   }
   .ellipsis-toggle {
     cursor: pointer;
   }
   /**
   Checked State
   **/

   .ellipsis-toggle input:checked + .ellipsis {
     display: none;
   }
   .ellipsis-toggle input:checked ~ .ellipsis-content {
     display: inline;
	 background-color: #ddd;
   }
   hr {
    border: none;
    height: 1px;
    background-color: black;
	margin: 0;
   }
  </style>
 </head>
 <body>
  <div>Tipset: <b>{{.TipSet.Key}}</b></div>
  <div>Epoch: {{.TipSet.Height}}</div>
  <div>State CID: <b>{{.Comp.Root}}</b></div>
  <div>Calls</div>
  {{range .Comp.Trace}}
   {{template "message" (Call .ExecutionTrace false .Msg.Cid.String)}}
  {{end}}
 </body>
</html>
`

var compStateMsg = `
<div class="exec" id="{{.Hash}}">
 {{$code := GetCode .Msg.To}}
 <div>
 <a href="#{{.Hash}}">
  {{if not .Subcall}}
   <h2 class="call">
  {{else}}
   <h4 class="call">
  {{end}}
   {{- CodeStr $code}}:{{GetMethod ($code) (.Msg.Method)}}
  {{if not .Subcall}}
   </h2>
  {{else}}
   </h4>
  {{end}}
 </a>
 </div>

 <div><b>{{.Msg.From}}</b> -&gt; <b>{{.Msg.To}}</b> ({{ToFil .Msg.Value}} STAR), M{{.Msg.Method}}</div>
 {{if not .Subcall}}<div><small>Msg CID: {{.Msg.Cid}}</small></div>{{end}}
 {{if gt (len .Msg.Params) 0}}
  <div><pre class="params">{{JsonParams ($code) (.Msg.Method) (.Msg.Params) | html}}</pre></div>
 {{end}}
 <div><span class="slow-{{IsSlow .Duration}}-{{IsVerySlow .Duration}}">Took {{.Duration}}</span>, <span class="exit{{IntExit .MsgRct.ExitCode}}">Exit: <b>{{.MsgRct.ExitCode}}</b></span>{{if gt (len .MsgRct.Return) 0}}, Return{{end}}</div>

 {{if gt (len .MsgRct.Return) 0}}
  <div><pre class="ret">{{JsonReturn ($code) (.Msg.Method) (.MsgRct.Return) | html}}</pre></div>
 {{end}}

 {{if ne .MsgRct.ExitCode 0}}
  <div class="error">Error: <pre>{{.Error}}</pre></div>
 {{end}}

<details>
<summary>Gas Trace</summary>
<table>
 <tr><th>Name</th><th>Total/Compute/Storage</th><th>Time Taken</th><th>Location</th></tr>
 {{define "virt" -}}
 {{- if . -}}
 <span class="deemp">+({{.}})</span>
 {{- end -}}
 {{- end}}

 {{define "gasC" -}}
 <td>{{.TotalGas}}{{template "virt" .TotalVirtualGas }}/{{.ComputeGas}}{{template "virt" .VirtualComputeGas}}/{{.StorageGas}}{{template "virt" .VirtualStorageGas}}</td>
 {{- end}}

 {{range .GasCharges}}
 <tr><td>{{.Name}}{{if .Extra}}:{{.Extra}}{{end}}</td>
 {{template "gasC" .}}
 <td>{{.TimeTaken}}</td>
  <td>
   {{ $fImp := FirstImportant .Location }}
   {{ if $fImp }}
   <details>
    <summary>{{ $fImp }}</summary><hr />
	{{ $elipOn := false }}
    {{ range $index, $ele := .Location -}}
     {{- if $index }}<br />{{end -}}
     {{- if .Show -}}
	  {{ if $elipOn }}
	   {{ $elipOn = false }}
       </span></label>
	  {{end}}

      {{- if .Important }}<b>{{end -}}
      {{- . -}}
      {{if .Important }}</b>{{end}}
     {{else}}
	  {{ if not $elipOn }}
	    {{ $elipOn = true }}
        <label class="ellipsis-toggle"><input type="checkbox" /><span class="ellipsis">[…]<br /></span>
		<span class="ellipsis-content">
	  {{end}}
      {{- "" -}}
      {{- . -}}
     {{end}}
    {{end}}
	{{ if $elipOn }}
	  {{ $elipOn = false }}
      </span></label>
	{{end}}
   </details>
  {{end}}
  </td></tr>
  {{end}}
  {{with SumGas .GasCharges}}
  <tr class="sum"><td><b>Sum</b></td>
  {{template "gasC" .}}
  <td>{{.TimeTaken}}</td>
  <td></td></tr>
  {{end}}
</table>
</details>


 {{if gt (len .Subcalls) 0}}
  <div>Subcalls:</div>
  {{$hash := .Hash}}
  {{range .Subcalls}}
   {{template "message" (Call . true (printf "%s-%s" $hash .Msg.Cid.String))}}
  {{end}}
 {{end}}
</div>`

type compStateHTMLIn struct {
	TipSet *types.TipSet
	Comp   *api.ComputeStateOutput
}

func ComputeStateHTMLTempl(w io.Writer, ts *types.TipSet, o *api.ComputeStateOutput, getCode func(addr address.Address) (cid.Cid, error)) error {
	t, err := template.New("compute_state").Funcs(map[string]interface{}{
		"GetCode":    getCode,
		"GetMethod":  getMethod,
		"ToFil":      toFil,
		"JsonParams": JsonParams,
		"JsonReturn": jsonReturn,
		"IsSlow":     isSlow,
		"IsVerySlow": isVerySlow,
		"IntExit":    func(i exitcode.ExitCode) int64 { return int64(i) },
		"SumGas":     sumGas,
		"CodeStr":    codeStr,
		"Call":       call,
		"FirstImportant": func(locs []types.Loc) *types.Loc {
			if len(locs) != 0 {
				for _, l := range locs {
					if l.Important() {
						return &l
					}
				}
				return &locs[0]
			}
			return nil
		},
	}).Parse(compStateTemplate)
	if err != nil {
		return err
	}
	t, err = t.New("message").Parse(compStateMsg)
	if err != nil {
		return err
	}

	return t.ExecuteTemplate(w, "compute_state", &compStateHTMLIn{
		TipSet: ts,
		Comp:   o,
	})
}

type callMeta struct {
	types.ExecutionTrace
	Subcall bool
	Hash    string
}

func call(e types.ExecutionTrace, subcall bool, hash string) callMeta {
	return callMeta{
		ExecutionTrace: e,
		Subcall:        subcall,
		Hash:           hash,
	}
}

func codeStr(c cid.Cid) string {
	cmh, err := multihash.Decode(c.Hash())
	if err != nil {
		panic(err)
	}
	return string(cmh.Digest)
}

func getMethod(code cid.Cid, method abi.MethodNum) string {
	return stmgr.MethodsMap[code][method].Name
}

func toFil(f types.BigInt) types.FIL {
	return types.FIL(f)
}

func isSlow(t time.Duration) bool {
	return t > 10*time.Millisecond
}

func isVerySlow(t time.Duration) bool {
	return t > 50*time.Millisecond
}

func sumGas(changes []*types.GasTrace) types.GasTrace {
	var out types.GasTrace
	for _, gc := range changes {
		out.TotalGas += gc.TotalGas
		out.ComputeGas += gc.ComputeGas
		out.StorageGas += gc.StorageGas

		out.TotalVirtualGas += gc.TotalVirtualGas
		out.VirtualComputeGas += gc.VirtualComputeGas
		out.VirtualStorageGas += gc.VirtualStorageGas
	}

	return out
}

func JsonParams(code cid.Cid, method abi.MethodNum, params []byte) (string, error) {
	methodMeta, found := stmgr.MethodsMap[code][method]
	if !found {
		return "", fmt.Errorf("method %d not found on actor %s", method, code)
	}
	re := reflect.New(methodMeta.Params.Elem())
	p := re.Interface().(cbg.CBORUnmarshaler)
	if err := p.UnmarshalCBOR(bytes.NewReader(params)); err != nil {
		return "", err
	}

	b, err := json.MarshalIndent(p, "", "  ")
	return string(b), err
}

func jsonReturn(code cid.Cid, method abi.MethodNum, ret []byte) (string, error) {
	methodMeta, found := stmgr.MethodsMap[code][method]
	if !found {
		return "", fmt.Errorf("method %d not found on actor %s", method, code)
	}
	re := reflect.New(methodMeta.Ret.Elem())
	p := re.Interface().(cbg.CBORUnmarshaler)
	if err := p.UnmarshalCBOR(bytes.NewReader(ret)); err != nil {
		return "", err
	}

	b, err := json.MarshalIndent(p, "", "  ")
	return string(b), err
}

var stateWaitMsgCmd = &cli.Command{
	Name:      "wait-msg",
	Usage:     "Wait for a message to appear on chain",
	ArgsUsage: "[messageCid]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "timeout",
			Value: "10m",
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("must specify message cid to wait for")
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		msg, err := cid.Decode(cctx.Args().First())
		if err != nil {
			return err
		}

		mw, err := api.StateWaitMsg(ctx, msg, build.MessageConfidence)
		if err != nil {
			return err
		}

		m, err := api.ChainGetMessage(ctx, msg)
		if err != nil {
			return err
		}

		fmt.Printf("message was executed in tipset: %s\n", mw.TipSet.Cids())
		fmt.Printf("Exit Code: %d\n", mw.Receipt.ExitCode)
		fmt.Printf("Gas Used: %d\n", mw.Receipt.GasUsed)
		fmt.Printf("Return: %x\n", mw.Receipt.Return)
		if err := printReceiptReturn(ctx, api, m, mw.Receipt); err != nil {
			return err
		}

		return nil
	},
}

func printReceiptReturn(ctx context.Context, api api.FullNode, m *types.Message, r types.MessageReceipt) error {
	act, err := api.StateGetActor(ctx, m.To, types.EmptyTSK)
	if err != nil {
		return err
	}

	jret, err := jsonReturn(act.Code, m.Method, r.Return)
	if err != nil {
		return err
	}

	fmt.Println(jret)

	return nil
}

var stateSearchMsgCmd = &cli.Command{
	Name:      "search-msg",
	Usage:     "Search to see whether a message has appeared on chain",
	ArgsUsage: "[messageCid]",
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("must specify message cid to search for")
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		msg, err := cid.Decode(cctx.Args().First())
		if err != nil {
			return err
		}

		mw, err := api.StateSearchMsg(ctx, msg)
		if err != nil {
			return err
		}

		if mw != nil {
			fmt.Printf("message was executed in tipset: %s", mw.TipSet.Cids())
			fmt.Printf("\nExit Code: %d", mw.Receipt.ExitCode)
			fmt.Printf("\nGas Used: %d", mw.Receipt.GasUsed)
			fmt.Printf("\nReturn: %x", mw.Receipt.Return)
		} else {
			fmt.Print("message was not found on chain")
		}
		return nil
	},
}

var stateCallCmd = &cli.Command{
	Name:      "call",
	Usage:     "Invoke a method on an actor locally",
	ArgsUsage: "[toAddress methodId <param1 param2 ...> (optional)]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "from",
			Usage: "",
			Value: builtin.SystemActorAddr.String(),
		},
		&cli.StringFlag{
			Name:  "value",
			Usage: "specify value field for invocation",
			Value: "0",
		},
		&cli.StringFlag{
			Name:  "ret",
			Usage: "specify how to parse output (auto, raw, addr, big)",
			Value: "auto",
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() < 2 {
			return fmt.Errorf("must specify at least actor and method to invoke")
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		toa, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return fmt.Errorf("given 'to' address %q was invalid: %w", cctx.Args().First(), err)
		}

		froma, err := address.NewFromString(cctx.String("from"))
		if err != nil {
			return fmt.Errorf("given 'from' address %q was invalid: %w", cctx.String("from"), err)
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		method, err := strconv.ParseUint(cctx.Args().Get(1), 10, 64)
		if err != nil {
			return fmt.Errorf("must pass method as a number")
		}

		value, err := types.ParseFIL(cctx.String("value"))
		if err != nil {
			return fmt.Errorf("failed to parse 'value': %s", err)
		}

		act, err := api.StateGetActor(ctx, toa, ts.Key())
		if err != nil {
			return fmt.Errorf("failed to lookup target actor: %s", err)
		}

		params, err := parseParamsForMethod(act.Code, method, cctx.Args().Slice()[2:])
		if err != nil {
			return fmt.Errorf("failed to parse params: %s", err)
		}

		ret, err := api.StateCall(ctx, &types.Message{
			From:   froma,
			To:     toa,
			Value:  types.BigInt(value),
			Method: abi.MethodNum(method),
			Params: params,
		}, ts.Key())
		if err != nil {
			return fmt.Errorf("state call failed: %s", err)
		}

		if ret.MsgRct.ExitCode != 0 {
			return fmt.Errorf("invocation failed (exit: %d, gasUsed: %d): %s", ret.MsgRct.ExitCode, ret.MsgRct.GasUsed, ret.Error)
		}

		s, err := formatOutput(cctx.String("ret"), ret.MsgRct.Return)
		if err != nil {
			return fmt.Errorf("failed to format output: %s", err)
		}

		fmt.Printf("gas used: %d\n", ret.MsgRct.GasUsed)
		fmt.Printf("return: %s\n", s)

		return nil
	},
}

func formatOutput(t string, val []byte) (string, error) {
	switch t {
	case "raw", "hex":
		return fmt.Sprintf("%x", val), nil
	case "address", "addr", "a":
		a, err := address.NewFromBytes(val)
		if err != nil {
			return "", err
		}
		return a.String(), nil
	case "big", "int", "bigint":
		bi := types.BigFromBytes(val)
		return bi.String(), nil
	case "fil":
		bi := types.FIL(types.BigFromBytes(val))
		return bi.String(), nil
	case "pid", "peerid", "peer":
		pid, err := peer.IDFromBytes(val)
		if err != nil {
			return "", err
		}

		return pid.Pretty(), nil
	case "auto":
		if len(val) == 0 {
			return "", nil
		}

		a, err := address.NewFromBytes(val)
		if err == nil {
			return "address: " + a.String(), nil
		}

		pid, err := peer.IDFromBytes(val)
		if err == nil {
			return "peerID: " + pid.Pretty(), nil
		}

		bi := types.BigFromBytes(val)
		return "bigint: " + bi.String(), nil
	default:
		return "", fmt.Errorf("unrecognized output type: %q", t)
	}
}

func parseParamsForMethod(act cid.Cid, method uint64, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}

	// TODO: consider moving this to a dedicated helper
	actMeta, ok := stmgr.MethodsMap[act]
	if !ok {
		return nil, fmt.Errorf("unknown actor %s", act)
	}

	methodMeta, ok := actMeta[abi.MethodNum(method)]
	if !ok {
		return nil, fmt.Errorf("unknown method %d for actor %s", method, act)
	}

	paramObj := methodMeta.Params
	if paramObj.NumField() != len(args) {
		return nil, fmt.Errorf("not enough arguments given to call that method (expecting %d)", paramObj.NumField())
	}

	p := reflect.New(paramObj)
	for i := 0; i < len(args); i++ {
		switch paramObj.Field(i).Type {
		case reflect.TypeOf(address.Address{}):
			a, err := address.NewFromString(args[i])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address: %s", err)
			}
			p.Elem().Field(i).Set(reflect.ValueOf(a))
		case reflect.TypeOf(uint64(0)):
			val, err := strconv.ParseUint(args[i], 10, 64)
			if err != nil {
				return nil, err
			}
			p.Elem().Field(i).Set(reflect.ValueOf(val))
		case reflect.TypeOf(abi.ChainEpoch(0)):
			val, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil {
				return nil, err
			}
			p.Elem().Field(i).Set(reflect.ValueOf(abi.ChainEpoch(val)))
		case reflect.TypeOf(big.Int{}):
			val, err := big.FromString(args[i])
			if err != nil {
				return nil, err
			}
			p.Elem().Field(i).Set(reflect.ValueOf(val))
		case reflect.TypeOf(peer.ID("")):
			pid, err := peer.Decode(args[i])
			if err != nil {
				return nil, fmt.Errorf("failed to parse peer ID: %s", err)
			}
			p.Elem().Field(i).Set(reflect.ValueOf(pid))
		default:
			return nil, fmt.Errorf("unsupported type for call (TODO): %s", paramObj.Field(i).Type)
		}
	}

	m := p.Interface().(cbg.CBORMarshaler)
	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return nil, fmt.Errorf("failed to marshal param object: %s", err)
	}
	return buf.Bytes(), nil
}

var stateCircSupplyCmd = &cli.Command{
	Name:  "circulating-supply",
	Usage: "Get the exact current circulating supply of Filecoin",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "vm-supply",
			Usage: "calculates the approximation of the circulating supply used internally by the VM (instead of the exact amount)",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		if cctx.IsSet("vm-supply") {
			circ, err := api.StateVMCirculatingSupplyInternal(ctx, ts.Key())
			if err != nil {
				return err
			}

			fmt.Println("Circulating supply: ", types.FIL(circ.FilCirculating))
			fmt.Println("Mined: ", types.FIL(circ.FilMined))
			fmt.Println("Vested: ", types.FIL(circ.FilVested))
			fmt.Println("Burnt: ", types.FIL(circ.FilBurnt))
			fmt.Println("Locked: ", types.FIL(circ.FilLocked))
		} else {
			circ, err := api.StateCirculatingSupply(ctx, ts.Key())
			if err != nil {
				return err
			}

			fmt.Println("Exact circulating supply: ", types.FIL(circ))
			return nil
		}

		return nil
	},
}

var stateSectorCmd = &cli.Command{
	Name:      "sector",
	Usage:     "Get miner sector info",
	ArgsUsage: "[miner address] [sector number]",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if cctx.Args().Len() != 2 {
			return xerrors.Errorf("expected 2 params")
		}

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		if ts == nil {
			ts, err = api.ChainHead(ctx)
			if err != nil {
				return err
			}
		}

		maddr, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		sid, err := strconv.ParseInt(cctx.Args().Get(1), 10, 64)
		if err != nil {
			return err
		}

		si, err := api.StateSectorGetInfo(ctx, maddr, abi.SectorNumber(sid), ts.Key())
		if err != nil {
			return err
		}

		fmt.Println("SectorNumber: ", si.SectorNumber)
		fmt.Println("SealProof: ", si.SealProof)
		fmt.Println("SealedCID: ", si.SealedCID)
		fmt.Println("DealIDs: ", si.DealIDs)
		fmt.Println()
		fmt.Println("Activation: ", EpochTime(ts.Height(), si.Activation))
		fmt.Println("Expiration: ", EpochTime(ts.Height(), si.Expiration))
		fmt.Println()
		fmt.Println("DealWeight: ", si.DealWeight)
		fmt.Println("VerifiedDealWeight: ", si.VerifiedDealWeight)
		fmt.Println("InitialPledge: ", types.FIL(si.InitialPledge))
		fmt.Println("ExpectedDayReward: ", types.FIL(si.ExpectedDayReward))
		fmt.Println("ExpectedStoragePledge: ", types.FIL(si.ExpectedStoragePledge))
		fmt.Println()

		sp, err := api.StateSectorPartition(ctx, maddr, abi.SectorNumber(sid), ts.Key())
		if err != nil {
			return err
		}

		fmt.Println("Deadline: ", sp.Deadline)
		fmt.Println("Partition: ", sp.Partition)

		return nil
	},
}

var stateMarketCmd = &cli.Command{
	Name:  "market",
	Usage: "Inspect the storage market actor",
	Subcommands: []*cli.Command{
		stateMarketBalanceCmd,
	},
}

var stateMarketBalanceCmd = &cli.Command{
	Name:  "balance",
	Usage: "Get the market balance (locked and escrowed) for a given account",
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return ShowHelp(cctx, fmt.Errorf("must specify address to print market balance for"))
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		ts, err := LoadTipSet(ctx, cctx, api)
		if err != nil {
			return err
		}

		addr, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		balance, err := api.StateMarketBalance(ctx, addr, ts.Key())
		if err != nil {
			return err
		}

		fmt.Printf("Escrow: %s\n", types.FIL(balance.Escrow))
		fmt.Printf("Locked: %s\n", types.FIL(balance.Locked))

		return nil
	},
}
