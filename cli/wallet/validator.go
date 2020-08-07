package wallet

import (
	"fmt"

	"github.com/nspcc-dev/neo-go/cli/flags"
	"github.com/nspcc-dev/neo-go/cli/options"
	"github.com/nspcc-dev/neo-go/pkg/crypto/keys"
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/rpc/client"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/opcode"
	"github.com/urfave/cli"
)

func newValidatorCommands() []cli.Command {
	return []cli.Command{
		{
			Name:      "vote",
			Usage:     "vote for a validator",
			UsageText: "vote -w <path> -r <rpc> [-s <timeout>] [-g gas] -a <addr> -c <public key>",
			Action:    handleVote,
			Flags: append([]cli.Flag{
				walletPathFlag,
				gasFlag,
				flags.AddressFlag{
					Name:  "address, a",
					Usage: "Address to vote from",
				},
				cli.StringFlag{
					Name:  "candidate, c",
					Usage: "Public key of candidate to vote for",
				},
			}, options.RPC...),
		},
	}
}

func handleVote(ctx *cli.Context) error {
	wall, err := openWallet(ctx.String("wallet"))
	if err != nil {
		return cli.NewExitError(err, 1)
	}

	addrFlag := ctx.Generic("address").(*flags.Address)
	addr := addrFlag.Uint160()
	acc := wall.GetAccount(addr)
	if acc == nil {
		return cli.NewExitError(fmt.Errorf("can't find account for the address: %s", addrFlag), 1)
	}

	var pub *keys.PublicKey
	pubStr := ctx.String("candidate")
	if pubStr != "" {
		pub, err = keys.NewPublicKeyFromString(pubStr)
		if err != nil {
			return cli.NewExitError(fmt.Errorf("invalid public key: '%s'", pubStr), 1)
		}
	}

	gctx, cancel := options.GetTimeoutContext(ctx)
	defer cancel()

	c, err := options.GetRPCClient(gctx, ctx)
	if err != nil {
		return err
	}

	var pubArg interface{}
	if pub != nil {
		pubArg = pub.Bytes()
	}

	gas := flags.Fixed8FromContext(ctx, "gas")
	w := io.NewBufBinWriter()
	emit.AppCallWithOperationAndArgs(w.BinWriter, client.NeoContractHash, "vote", addr.BytesBE(), pubArg)
	emit.Opcode(w.BinWriter, opcode.ASSERT)

	tx, err := c.CreateTxFromScript(w.Bytes(), acc, int64(gas))
	if err != nil {
		return cli.NewExitError(err, 1)
	}

	if pass, err := readPassword("Password > "); err != nil {
		return cli.NewExitError(err, 1)
	} else if err := acc.Decrypt(pass); err != nil {
		return cli.NewExitError(err, 1)
	}

	if err = acc.SignTx(tx); err != nil {
		return cli.NewExitError(fmt.Errorf("can't sign tx: %v", err), 1)
	}

	res, err := c.SendRawTransaction(tx)
	if err != nil {
		return cli.NewExitError(err, 1)
	}
	fmt.Println(res.StringLE())
	return nil
}
