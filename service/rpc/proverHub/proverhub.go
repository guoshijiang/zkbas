package main

import (
	"flag"

	"github.com/consensys/gnark/backend/groth16"
	"github.com/robfig/cron/v3"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/service"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/zecrey-labs/zecrey-legend/common/model/proofSender"
	"github.com/zecrey-labs/zecrey-legend/common/tree"
	"github.com/zecrey-labs/zecrey-legend/common/util"
	"github.com/zecrey-labs/zecrey-legend/service/rpc/proverHub/internal/config"
	"github.com/zecrey-labs/zecrey-legend/service/rpc/proverHub/internal/logic"
	"github.com/zecrey-labs/zecrey-legend/service/rpc/proverHub/internal/server"
	"github.com/zecrey-labs/zecrey-legend/service/rpc/proverHub/internal/svc"
	"github.com/zecrey-labs/zecrey-legend/service/rpc/proverHub/proverHubProto"
)

var configFile = flag.String("f",
	"./etc/proverhub.yaml", "the config file")

func main() {
	flag.Parse()

	var c config.Config
	conf.MustLoad(*configFile, &c)
	ctx := svc.NewServiceContext(c)
	srv := server.NewProverHubRPCServer(ctx)
	logx.DisableStat()

	p, err := ctx.ProofSenderModel.GetLatestConfirmedProof()
	if err != nil {
		if err != proofSender.ErrNotFound {
			logx.Error("[prover] => GetLatestConfirmedProof error:", err)
			return
		} else {
			p = &proofSender.ProofSender{
				BlockNumber: 0,
			}
		}
	}
	var (
		accountTree   *tree.Tree
		assetTrees    []*tree.Tree
		liquidityTree *tree.Tree
		nftTree       *tree.Tree
	)
	// init accountTree and accountStateTrees
	// the init block number use the latest sent block
	accountTree, assetTrees, err = tree.InitAccountTree(
		ctx.AccountModel,
		ctx.AccountHistoryModel,
		p.BlockNumber,
	)
	// the blockHeight depends on the proof start position
	if err != nil {
		logx.Error("[prover] => InitMerkleTree error:", err)
		return
	}

	liquidityTree, err = tree.InitLiquidityTree(ctx.LiquidityHistoryModel, p.BlockNumber)
	if err != nil {
		logx.Errorf("[prover] InitLiquidityTree error: %s", err)
		return
	}
	nftTree, err = tree.InitNftTree(ctx.NftHistoryModel, p.BlockNumber)
	if err != nil {
		logx.Errorf("[prover] InitNftTree error: %s", err)
		return
	}

	logic.VerifyingKeyPath = c.KeyPath.VerifyingKeyPath
	logic.VerifyingKeyTxsCount = c.KeyPath.VerifyingKeyTxsCount
	if len(logic.VerifyingKeyTxsCount) != len(logic.VerifyingKeyPath) {
		logx.Errorf("VerifyingKeyPath doesn't match VerifyingKeyTxsCount")
		return
	}

	logx.Info("start reading verifying key")
	logic.VerifyingKeys = make([]groth16.VerifyingKey, len(logic.VerifyingKeyPath))
	for i := 0; i < len(logic.VerifyingKeyPath); i++ {
		logic.VerifyingKeys[i], err = util.LoadVerifyingKey(logic.VerifyingKeyPath[i])
		if err != nil {
			logx.Errorf("LoadVerifyingKey %d Error: %s", i, err.Error())
			return
		}
	}

	cronJob := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DiscardLogger),
	))
	_, err = cronJob.AddFunc("@every 10s", func() {
		// cron job for creating cryptoBlock
		logx.Info("==========start handle crypto block==========")
		logic.HandleCryptoBlock(
			accountTree,
			&assetTrees,
			liquidityTree,
			nftTree,
			ctx,
			10, // TODO
		)
		logx.Info("==========end handle crypto block==========")
	})
	if err != nil {
		panic(err)
	}
	cronJob.Start()

	s := zrpc.MustNewServer(c.RpcServerConf, func(grpcServer *grpc.Server) {
		proverHubProto.RegisterProverHubRPCServer(grpcServer, srv)

		if c.Mode == service.DevMode || c.Mode == service.TestMode {
			reflection.Register(grpcServer)
		}
	})
	defer s.Stop()

	logx.Infof("Starting rpc server at %s...\n", c.ListenOn)
	s.Start()
}
