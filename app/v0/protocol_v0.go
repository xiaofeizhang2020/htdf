package v0

import (
	"fmt"
	"sort"

	"github.com/orientwalt/htdf/app/protocol"
	"github.com/orientwalt/htdf/codec"
	sdk "github.com/orientwalt/htdf/types"
	"github.com/orientwalt/htdf/x/auth"
	"github.com/orientwalt/htdf/x/bank"
	htdfservice "github.com/orientwalt/htdf/x/core"
	"github.com/orientwalt/htdf/x/crisis"
	distr "github.com/orientwalt/htdf/x/distribution"
	"github.com/orientwalt/htdf/x/gov"
	"github.com/orientwalt/htdf/x/guardian"
	"github.com/orientwalt/htdf/x/mint"
	"github.com/orientwalt/htdf/x/params"
	"github.com/orientwalt/htdf/x/service"
	"github.com/orientwalt/htdf/x/slashing"
	stake "github.com/orientwalt/htdf/x/staking"
	"github.com/orientwalt/htdf/x/upgrade"
	abci "github.com/orientwalt/tendermint/abci/types"
	cfg "github.com/orientwalt/tendermint/config"
	"github.com/orientwalt/tendermint/libs/log"
)

const (
	//
	RouterKey = "htdfservice"
)

var _ protocol.Protocol = (*ProtocolV0)(nil)

type ProtocolV0 struct {
	version        uint64
	cdc            *codec.Codec
	logger         log.Logger
	invCheckPeriod uint

	// Manage getting and setting accounts
	accountMapper       auth.AccountKeeper
	feeCollectionKeeper auth.FeeCollectionKeeper
	bankKeeper          bank.Keeper
	StakeKeeper         stake.Keeper
	slashingKeeper      slashing.Keeper
	mintKeeper          mint.Keeper
	distrKeeper         distr.Keeper
	protocolKeeper      sdk.ProtocolKeeper
	govKeeper           gov.Keeper
	paramsKeeper        params.Keeper
	serviceKeeper       service.Keeper
	guardianKeeper      guardian.Keeper
	upgradeKeeper       upgrade.Keeper
	crisisKeeper        crisis.Keeper

	router      protocol.Router      // handle any kind of message
	queryRouter protocol.QueryRouter // router for redirecting query calls

	anteHandler          sdk.AnteHandler          // ante handler for fee and auth
	feeRefundHandler     sdk.FeeRefundHandler     // fee handler for fee refund
	feePreprocessHandler sdk.FeePreprocessHandler // fee handler for fee preprocessor

	// may be nil
	initChainer  sdk.InitChainer1 // initialize state with validators and state blob
	beginBlocker sdk.BeginBlocker // logic to run before any txs
	endBlocker   sdk.EndBlocker   // logic to run after all txs, and to determine valset changes
	config       *cfg.InstrumentationConfig

	metrics *Metrics
}

func NewProtocolV0(version uint64, log log.Logger, pk sdk.ProtocolKeeper, invCheckPeriod uint, config *cfg.InstrumentationConfig) *ProtocolV0 {
	p0 := ProtocolV0{
		version:        version,
		logger:         log,
		protocolKeeper: pk,
		invCheckPeriod: invCheckPeriod,
		router:         protocol.NewRouter(),
		queryRouter:    protocol.NewQueryRouter(),
		config:         config,
		metrics:        PrometheusMetrics(config),
	}
	return &p0
}

// load the configuration of this Protocol
func (p *ProtocolV0) Load() {
	p.configCodec()
	p.configKeepers()
	p.configRouters()
	p.configFeeHandlers()
	p.configParams()
}

// verison0 don't need the init
func (p *ProtocolV0) Init() {

}

// verison0 tx codec
func (p *ProtocolV0) GetCodec() *codec.Codec {
	return p.cdc
}

func (p *ProtocolV0) InitMetrics(store sdk.CommitMultiStore) {
	p.StakeKeeper.InitMetrics(store.GetKVStore(protocol.KeyStake))
	p.serviceKeeper.InitMetrics(store.GetKVStore(protocol.KeyService))
}

func (p *ProtocolV0) configCodec() {
	p.cdc = MakeLatestCodec()
}

func MakeLatestCodec() *codec.Codec {
	var cdc = codec.New()
	htdfservice.RegisterCodec(cdc)
	params.RegisterCodec(cdc) // only used by querier
	mint.RegisterCodec(cdc)   // only used by querier
	bank.RegisterCodec(cdc)
	stake.RegisterCodec(cdc)
	distr.RegisterCodec(cdc)
	slashing.RegisterCodec(cdc)
	gov.RegisterCodec(cdc)
	upgrade.RegisterCodec(cdc)
	service.RegisterCodec(cdc)
	guardian.RegisterCodec(cdc)
	auth.RegisterCodec(cdc)
	sdk.RegisterCodec(cdc)
	codec.RegisterCrypto(cdc)
	crisis.RegisterCodec(cdc)
	return cdc
}

func (p *ProtocolV0) GetVersion() uint64 {
	return p.version
}

func (p *ProtocolV0) ValidateTx(ctx sdk.Context, txBytes []byte, msgs []sdk.Msg) sdk.Error {

	serviceMsgNum := 0
	for _, msg := range msgs {
		if msg.Route() == service.MsgRoute {
			serviceMsgNum++
		}
	}
	fmt.Println("1111111111@@@@@@@@@@@@@!!!!!!!!!")
	if serviceMsgNum != 0 && serviceMsgNum != len(msgs) {
		return sdk.ErrServiceTxLimit("Can't mix service msgs with other types of msg in one transaction!")
	}

	if serviceMsgNum == 0 {
		subspace, found := p.paramsKeeper.GetSubspace(auth.DefaultParamspace)
		var txSizeLimit uint64
		if found {
			fmt.Println("22222222222@@@@@@@@@@@@@!!!!!!!!!", subspace)
			subspace.Get(ctx, auth.KeySigVerifyCostSecp256k1, &txSizeLimit)
		} else {
			panic("The subspace " + auth.DefaultParamspace + " cannot be found!")
		}
		fmt.Println("33333333333@@@@@@@@@@@@@!!!!!!!!!")
		if uint64(len(txBytes)) > txSizeLimit {
			return sdk.ErrExceedsTxSize(fmt.Sprintf("the tx size [%d] exceeds the limitation [%d]", len(txBytes), txSizeLimit))
		}
	}

	if serviceMsgNum == len(msgs) {
		subspace, found := p.paramsKeeper.GetSubspace(service.DefaultParamSpace)
		var serviceTxSizeLimit uint64
		if found {
			subspace.Get(ctx, service.KeyTxSizeLimit, &serviceTxSizeLimit)
		} else {
			panic("The subspace " + service.DefaultParamSpace + " cannot be found!")
		}

		if uint64(len(txBytes)) > serviceTxSizeLimit {
			return sdk.ErrExceedsTxSize(fmt.Sprintf("the tx size [%d] exceeds the limitation [%d]", len(txBytes), serviceTxSizeLimit))
		}

	}

	return nil
}

// create all Keepers
func (p *ProtocolV0) configKeepers() {
	p.paramsKeeper = params.NewKeeper(
		p.cdc,
		protocol.KeyParams, protocol.TkeyParams,
	)

	// define the AccountKeeper
	p.accountMapper = auth.NewAccountKeeper(
		p.cdc,
		protocol.KeyAccount, // target store
		p.paramsKeeper.Subspace(auth.DefaultParamspace),
		auth.ProtoBaseAccount, // prototype
	)

	// add handlers
	p.guardianKeeper = guardian.NewKeeper(
		p.cdc,
		protocol.KeyGuardian,
		guardian.DefaultCodespace,
	)

	p.bankKeeper = bank.NewBaseKeeper(
		p.accountMapper,
		p.paramsKeeper.Subspace(bank.DefaultParamspace),
		bank.DefaultCodespace,
	)

	p.feeCollectionKeeper = auth.NewFeeCollectionKeeper(
		p.cdc,
		protocol.KeyFee,
	)

	stakeKeeper := stake.NewKeeper(
		p.cdc,
		protocol.KeyStake, protocol.TkeyStake,
		p.bankKeeper, p.paramsKeeper.Subspace(stake.DefaultParamspace),
		stake.DefaultCodespace,
		stake.PrometheusMetrics(p.config),
	)
	p.mintKeeper = mint.NewKeeper(p.cdc, protocol.KeyMint,
		p.paramsKeeper.Subspace(mint.DefaultParamspace),
		&stakeKeeper, p.feeCollectionKeeper,
	)
	p.distrKeeper = distr.NewKeeper(
		p.cdc,
		protocol.KeyDistr,
		p.paramsKeeper.Subspace(distr.DefaultParamspace),
		p.bankKeeper, &stakeKeeper, p.feeCollectionKeeper,
		distr.DefaultCodespace, distr.PrometheusMetrics(p.config),
	)
	p.slashingKeeper = slashing.NewKeeper(
		p.cdc,
		protocol.KeySlashing,
		&stakeKeeper, p.paramsKeeper.Subspace(slashing.DefaultParamspace),
		slashing.DefaultCodespace,
		slashing.PrometheusMetrics(p.config),
	)

	p.govKeeper = gov.NewKeeper(
		p.cdc,
		protocol.KeyGov,
		p.paramsKeeper,
		p.protocolKeeper,
		p.guardianKeeper,
		p.paramsKeeper.Subspace(gov.DefaultParamspace),
		p.bankKeeper,
		&stakeKeeper,
		gov.DefaultCodespace,
	)

	p.serviceKeeper = service.NewKeeper(
		p.cdc,
		protocol.KeyService,
		p.bankKeeper,
		p.guardianKeeper,
		service.DefaultCodespace,
		p.paramsKeeper.Subspace(service.DefaultParamSpace),
		service.PrometheusMetrics(p.config),
	)

	// register the staking hooks
	// NOTE: StakeKeeper above are passed by reference,
	// so that it can be modified like below:
	p.StakeKeeper = *stakeKeeper.SetHooks(
		NewHooks(p.distrKeeper.Hooks(), p.slashingKeeper.Hooks()))

	p.upgradeKeeper = upgrade.NewKeeper(p.cdc, protocol.KeyUpgrade, p.protocolKeeper, p.StakeKeeper, upgrade.PrometheusMetrics(p.config))
}

// configure all Routers
func (p *ProtocolV0) configRouters() {
	// register the crisis routes
	bank.RegisterInvariants(&p.crisisKeeper, p.accountMapper)
	distr.RegisterInvariants(&p.crisisKeeper, p.distrKeeper, p.StakeKeeper)
	stake.RegisterInvariants(&p.crisisKeeper, p.StakeKeeper, p.feeCollectionKeeper, p.distrKeeper, p.accountMapper)

	p.router.
		AddRoute(RouterKey, htdfservice.NewHandler(p)).
		AddRoute(protocol.BankRoute, bank.NewHandler(p.bankKeeper)).
		AddRoute(protocol.StakeRoute, stake.NewHandler(p.StakeKeeper)).
		AddRoute(protocol.SlashingRoute, slashing.NewHandler(p.slashingKeeper)).
		AddRoute(protocol.DistrRoute, distr.NewHandler(p.distrKeeper)).
		AddRoute(protocol.GovRoute, gov.NewHandler(p.govKeeper)).
		AddRoute(protocol.ServiceRoute, service.NewHandler(p.serviceKeeper)).
		AddRoute(protocol.GuardianRoute, guardian.NewHandler(p.guardianKeeper)).
		AddRoute(crisis.RouterKey, crisis.NewHandler(p.crisisKeeper))

	p.queryRouter.
		AddRoute(protocol.AccountRoute, auth.NewQuerier(p.accountMapper)).
		AddRoute(protocol.GovRoute, gov.NewQuerier(p.govKeeper)).
		AddRoute(protocol.StakeRoute, stake.NewQuerier(p.StakeKeeper, p.cdc)).
		AddRoute(protocol.DistrRoute, distr.NewQuerier(p.distrKeeper)).
		AddRoute(protocol.GuardianRoute, guardian.NewQuerier(p.guardianKeeper)).
		AddRoute(protocol.ServiceRoute, service.NewQuerier(p.serviceKeeper)).
		AddRoute(protocol.ParamsRoute, params.NewQuerier(p.paramsKeeper))
}

// configure all Stores
func (p *ProtocolV0) configFeeHandlers() {
	p.anteHandler = auth.NewAnteHandler(p.accountMapper, p.feeCollectionKeeper)
	p.feeRefundHandler = auth.NewFeeRefundHandler(p.accountMapper, p.feeCollectionKeeper)
	//p.feePreprocessHandler = auth.NewFeePreprocessHandler(p.feeCollectionKeeper)
}

// configure all Stores
func (p *ProtocolV0) GetKVStoreKeyList() []*sdk.KVStoreKey {
	return []*sdk.KVStoreKey{
		protocol.KeyMain,
		protocol.KeyAccount,
		protocol.KeyStake,
		protocol.KeyMint,
		protocol.KeyDistr,
		protocol.KeySlashing,
		protocol.KeyGov,
		protocol.KeyFee,
		protocol.KeyParams,
		protocol.KeyUpgrade,
		protocol.KeyService,
		protocol.KeyGuardian}
}

// configure all Stores
func (p *ProtocolV0) configParams() {

	p.paramsKeeper.RegisterParamSet(&mint.Params{}, &slashing.Params{}, &service.Params{}, &auth.Params{}, &stake.Params{}, &distr.Params{})

}

// application updates every end block
func (p *ProtocolV0) BeginBlocker(ctx sdk.Context, req abci.RequestBeginBlock) abci.ResponseBeginBlock {
	// mint new tokens for this new block
	mint.BeginBlocker(ctx, p.mintKeeper)

	// distribute rewards from previous block
	distr.BeginBlocker(ctx, req, p.distrKeeper)

	tags := slashing.BeginBlocker(ctx, req, p.slashingKeeper)
	fmt.Println("------------------BeginBlocker---------------------")
	return abci.ResponseBeginBlock{
		Tags: tags.ToKVPairs(),
	}
}

// application updates every end block
func (p *ProtocolV0) EndBlocker(ctx sdk.Context, req abci.RequestEndBlock) abci.ResponseEndBlock {
	tags := gov.EndBlocker(ctx, p.govKeeper)
	tags = tags.AppendTags(slashing.EndBlocker(ctx, req, p.slashingKeeper))
	tags = tags.AppendTags(service.EndBlocker(ctx, p.serviceKeeper))
	tags = tags.AppendTags(upgrade.EndBlocker(ctx, p.upgradeKeeper))
	validatorUpdates, endBlockerTags := stake.EndBlocker(ctx, p.StakeKeeper)
	tags = append(tags, endBlockerTags...)
	if p.invCheckPeriod != 0 && ctx.BlockHeight()%int64(p.invCheckPeriod) == 0 {
		p.assertRuntimeInvariants(ctx)
	}
	fmt.Println("------------------EndBlocker---------------------")
	return abci.ResponseEndBlock{
		ValidatorUpdates: validatorUpdates,
		Tags:             tags,
	}
}

// initialize store from a genesis state
func (p *ProtocolV0) initFromGenesisState(ctx sdk.Context, DeliverTx sdk.DeliverTx, genesisState GenesisState) []abci.ValidatorUpdate {
	genesisState.Sanitize()

	// load the accounts
	for _, gacc := range genesisState.Accounts {
		acc := gacc.ToAccount()
		acc.AccountNumber = p.accountMapper.GetNextAccountNumber(ctx)
		p.accountMapper.SetGenesisAccount(ctx, acc)
	}

	// initialize distribution (must happen before staking)
	distr.InitGenesis(ctx, p.distrKeeper, genesisState.DistrData)

	// load the initial stake information
	validators, err := stake.InitGenesis(ctx, p.StakeKeeper, genesisState.StakeData)
	if err != nil {
		panic(err)
	}

	// initialize module-specific stores
	gov.InitGenesis(ctx, p.govKeeper, genesisState.GovData)
	auth.InitGenesis(ctx, p.accountMapper, p.feeCollectionKeeper, genesisState.AuthData)
	slashing.InitGenesis(ctx, p.slashingKeeper, genesisState.SlashingData, genesisState.StakeData.Validators.ToSDKValidators())
	mint.InitGenesis(ctx, p.mintKeeper, genesisState.MintData)
	service.InitGenesis(ctx, p.serviceKeeper, genesisState.ServiceData)
	guardian.InitGenesis(ctx, p.guardianKeeper, genesisState.GuardianData)
	upgrade.InitGenesis(ctx, p.upgradeKeeper, genesisState.UpgradeData)

	// validate genesis state
	if err := IrisValidateGenesisState(genesisState); err != nil {
		panic(err) // TODO find a way to do this w/o panics
	}

	if len(genesisState.GenTxs) > 0 {
		for _, genTx := range genesisState.GenTxs {
			var tx auth.StdTx
			err = p.cdc.UnmarshalJSON(genTx, &tx)
			if err != nil {
				panic(err)
			}
			bz := p.cdc.MustMarshalBinaryLengthPrefixed(tx)
			res := DeliverTx(bz)
			if !res.IsOK() {
				panic(res.Log)
			}
		}
		validators = p.StakeKeeper.ApplyAndReturnValidatorSetUpdates(ctx)
	}
	fmt.Println("999999999999999")
	return validators
}

// custom logic for iris initialization
// just 0 version need Initchainer
func (p *ProtocolV0) InitChainer(ctx sdk.Context, DeliverTx sdk.DeliverTx, req abci.RequestInitChain) abci.ResponseInitChain {
	stateJSON := req.AppStateBytes
	fmt.Println("################	", stateJSON)
	var genesisState GenesisState
	err := p.cdc.UnmarshalJSON(stateJSON, &genesisState)
	if err != nil {
		panic(err) // TODO https://github.com/cosmos/cosmos-sdk/issues/468
		// return sdk.ErrGenesisParse("").TraceCause(err, "")
	}
	fmt.Println("@@@@@@@@@@@@@@@@	", genesisState.Accounts)
	validators := p.initFromGenesisState(ctx, DeliverTx, genesisState)

	// sanity check
	if len(req.Validators) > 0 {
		if len(req.Validators) != len(validators) {
			panic(fmt.Errorf("len(RequestInitChain.Validators) != len(validators) (%d != %d)",
				len(req.Validators), len(validators)))
		}
		sort.Sort(abci.ValidatorUpdates(req.Validators))
		sort.Sort(abci.ValidatorUpdates(validators))
		for i, val := range validators {
			if !val.Equal(req.Validators[i]) {
				panic(fmt.Errorf("validators[%d] != req.Validators[%d] ", i, i))
			}
		}
	}

	// assert runtime invariants
	p.assertRuntimeInvariants(ctx)
	fmt.Println("------------------InitChainer---------------------")
	return abci.ResponseInitChain{
		Validators: validators,
	}
}

func (p *ProtocolV0) Transition(ctx sdk.Context, inputMsg sdk.Msg) (result sdk.Result) {
	// var sendTxResp apptypes.SendTxResp

	// switch msg := inputMsg.(type) {

	// case htdfservice.MsgSendFrom:

	// 	// classic transfer
	// 	if len(msg.Data) == 0 {
	// 		return htdfservice.HandleMsgSendFrom(ctx, app.bankKeeper, msg)
	// 	}

	// 	fmt.Printf("FeeTotal1=%v\n", app.feeCollectionKeeper.GetCollectedFees(ctx))

	// 	if !msg.To.Empty() {
	// 		//open smart contract
	// 		fmt.Printf("openContract\n")
	// 		genErr, evmOutput := app.openContract(ctx, msg)
	// 		if genErr != nil {
	// 			fmt.Printf("openContract error|err=%s\n", genErr)
	// 			sendTxResp.ErrCode = apptypes.ErrCode_OpenContract
	// 			return sdk.Result{Code: sendTxResp.ErrCode, Log: sendTxResp.String()}
	// 		}

	// 		sendTxResp.EvmOutput = evmOutput
	// 		return sdk.Result{Code: sendTxResp.ErrCode, Log: sendTxResp.String()}

	// 	} else {
	// 		// new smart contract
	// 		fmt.Printf("create contract\n")
	// 		err, contractAddr := app.CreateContract(ctx, msg)
	// 		if err != nil {
	// 			fmt.Printf("CreateContract error|err=%s\n", err)
	// 			sendTxResp.ErrCode = apptypes.ErrCode_CreateContract
	// 			return sdk.Result{Code: sendTxResp.ErrCode, Log: sendTxResp.String()}
	// 		}

	// 		sendTxResp.ContractAddress = contractAddr
	// 		return sdk.Result{Code: sendTxResp.ErrCode, Log: sendTxResp.String()}
	// 	}

	// 	fmt.Printf("FeeTotal2=%v\n", app.feeCollectionKeeper.GetCollectedFees(ctx))

	// case htdfservice.MsgAdd:
	// 	return htdfservice.HandleMsgAdd(ctx, app.bankKeeper, msg)
	// default:
	// 	fmt.Printf("msgType error|mstType=%v\n", msg.Type())
	// 	sendTxResp.ErrCode = apptypes.ErrCode_Param
	// 	return sdk.Result{Code: sendTxResp.ErrCode, Log: sendTxResp.String()}
	// }

	return sdk.Result{}
}

func (p *ProtocolV0) GetRouter() protocol.Router {
	return p.router
}
func (p *ProtocolV0) GetQueryRouter() protocol.QueryRouter {
	return p.queryRouter
}
func (p *ProtocolV0) GetAnteHandler() sdk.AnteHandler {
	return p.anteHandler
}
func (p *ProtocolV0) GetFeeRefundHandler() sdk.FeeRefundHandler {
	return p.feeRefundHandler
}
func (p *ProtocolV0) GetFeePreprocessHandler() sdk.FeePreprocessHandler {
	return p.feePreprocessHandler
}
func (p *ProtocolV0) GetInitChainer() sdk.InitChainer1 {
	return p.InitChainer
}
func (p *ProtocolV0) GetBeginBlocker() sdk.BeginBlocker {
	return p.BeginBlocker
}
func (p *ProtocolV0) GetEndBlocker() sdk.EndBlocker {
	return p.EndBlocker
}

// Combined Staking Hooks
type Hooks struct {
	dh distr.Hooks
	sh slashing.Hooks
}

func NewHooks(dh distr.Hooks, sh slashing.Hooks) Hooks {
	return Hooks{dh, sh}
}

var _ sdk.StakingHooks = Hooks{}

// nolint
func (h Hooks) AfterValidatorCreated(ctx sdk.Context, valAddr sdk.ValAddress) {
	h.dh.AfterValidatorCreated(ctx, valAddr)
	h.sh.AfterValidatorCreated(ctx, valAddr)
}
func (h Hooks) BeforeValidatorModified(ctx sdk.Context, valAddr sdk.ValAddress) {
	h.dh.BeforeValidatorModified(ctx, valAddr)
	h.sh.BeforeValidatorModified(ctx, valAddr)
}
func (h Hooks) AfterValidatorRemoved(ctx sdk.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) {
	h.dh.AfterValidatorRemoved(ctx, consAddr, valAddr)
	h.sh.AfterValidatorRemoved(ctx, consAddr, valAddr)
}
func (h Hooks) AfterValidatorBonded(ctx sdk.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) {
	h.dh.AfterValidatorBonded(ctx, consAddr, valAddr)
	h.sh.AfterValidatorBonded(ctx, consAddr, valAddr)
}
func (h Hooks) AfterValidatorBeginUnbonding(ctx sdk.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) {
	h.dh.AfterValidatorBeginUnbonding(ctx, consAddr, valAddr)
	h.sh.AfterValidatorBeginUnbonding(ctx, consAddr, valAddr)
}
func (h Hooks) BeforeDelegationCreated(ctx sdk.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) {
	h.dh.BeforeDelegationCreated(ctx, delAddr, valAddr)
	h.sh.BeforeDelegationCreated(ctx, delAddr, valAddr)
}
func (h Hooks) BeforeDelegationSharesModified(ctx sdk.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) {
	h.dh.BeforeDelegationSharesModified(ctx, delAddr, valAddr)
	h.sh.BeforeDelegationSharesModified(ctx, delAddr, valAddr)
}
func (h Hooks) BeforeDelegationRemoved(ctx sdk.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) {
	h.dh.BeforeDelegationRemoved(ctx, delAddr, valAddr)
	h.sh.BeforeDelegationRemoved(ctx, delAddr, valAddr)
}
func (h Hooks) AfterDelegationModified(ctx sdk.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) {
	h.dh.AfterDelegationModified(ctx, delAddr, valAddr)
	h.sh.AfterDelegationModified(ctx, delAddr, valAddr)
}
func (h Hooks) BeforeValidatorSlashed(ctx sdk.Context, valAddr sdk.ValAddress, fraction sdk.Dec) {
	h.dh.BeforeValidatorSlashed(ctx, valAddr, fraction)
	h.sh.BeforeValidatorSlashed(ctx, valAddr, fraction)
}