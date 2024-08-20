package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	associatedtokenaccount "pump-fun-project/programs/associated-token-account"
	computebudget "pump-fun-project/programs/compute-budget"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/ws"
)

func bufferFromUInt64(value uint64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, value)
	return buf
}

// Helper function to fetch token data from Pump.fun API
func getTokenData(tokenAddress string) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://frontend-api.pump.fun/coins/%s", tokenAddress)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// SendAndConfirmTransaction sends a transaction and waits for confirmation.
func SendAndConfirmTransaction(
	ctx context.Context,
	rpcClient *rpc.Client,
	wsClient *ws.Client,
	transaction *solana.Transaction,
) (solana.Signature, error) {
	opts := rpc.TransactionOpts{
		SkipPreflight:       false,
		PreflightCommitment: rpc.CommitmentFinalized,
	}

	sig, err := rpcClient.SendTransactionWithOpts(
		ctx,
		transaction,
		opts,
	)
	if err != nil {
		return sig, err
	}

	log.Println("Transaction sent with signature:", sig.String())

	// Fetch the transaction details after sending it
	txStatus, err := rpcClient.GetTransaction(
		context.TODO(),
		sig,
		&rpc.GetTransactionOpts{
			Commitment: rpc.CommitmentFinalized,
		},
	)
	if err != nil {
		log.Printf("Error fetching transaction status: %v", err)
	} else {
		log.Printf("Transaction status: %+v", txStatus)
	}

	_, err = WaitForConfirmation(ctx, wsClient, sig, nil)
	return sig, err
}

// WaitForConfirmation waits for a transaction to be confirmed.
func WaitForConfirmation(
	ctx context.Context,
	wsClient *ws.Client,
	sig solana.Signature,
	timeout *time.Duration,
) (bool, error) {
	sub, err := wsClient.SignatureSubscribe(
		sig,
		rpc.CommitmentFinalized,
	)
	if err != nil {
		return false, err
	}
	defer sub.Unsubscribe()

	if timeout == nil {
		t := 2 * time.Minute
		timeout = &t
	}

	log.Println("Waiting for transaction confirmation...")

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(*timeout):
			return false, fmt.Errorf("timeout")
		case resp, ok := <-sub.Response():
			if !ok {
				return false, fmt.Errorf("subscription closed")
			}
			if resp.Value.Err != nil {
				return true, fmt.Errorf("confirmed transaction with execution error: %v", resp.Value.Err)
			} else {
				log.Println("Transaction confirmed:", sig.String())
				return true, nil
			}
		case err := <-sub.Err():
			return false, err
		}
	}
}

func main() {
	base58PrivateKey := "3bf9aUPWe41kj6eyxsoxFaKwAaCfsY4uFrbuCDuaSqiUv1XVP1GHz6MpjNBRUDfnnqFNuDGpHhUaKEFwV78TF56B"

	privateKey, err := solana.PrivateKeyFromBase58(base58PrivateKey)
	if err != nil {
		log.Fatalf("Failed to decode base58 private key, err: %v", err)
	}

	owner := privateKey.PublicKey()

	rpcClient := rpc.New("https://mainnet.helius-rpc.com/?api-key=e83021f7-364e-44ca-9cbe-3b15c83ff7c7")
	wsClient, err := ws.Connect(context.Background(), "wss://mainnet.helius-rpc.com/?api-key=e83021f7-364e-44ca-9cbe-3b15c83ff7c7")
	if err != nil {
		log.Fatalf("Failed to connect to websocket, err: %v", err)
	}

	log.Println("Connected to Helius RPC")
	log.Println("wsClient", wsClient)

	tokenMint := solana.MustPublicKeyFromBase58("7TvKZn98Qrq3Nr5LSYgKrxF2muc4VPMWVvNEQ9TGf2Px")
	// tokenMint := solana.MustPublicKeyFromBase58("8n72HqDeRQQB8sfgE2oA1Lz7HKQJmhoNiDwoFcrqpump")
	feeRecipient := solana.MustPublicKeyFromBase58("CebN5WGQ4jvEPvsVU4EoHEpgzq1VV7AbicfhtW4xC9iM")
	global := solana.MustPublicKeyFromBase58("4wTV1YmiEkRvAtNtsSGPtUrqRYQMe5SKy2uB4Jjaxnjf")
	pumpFunProgram := solana.MustPublicKeyFromBase58("6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P")
	pumpFunAccount := solana.MustPublicKeyFromBase58("Ce6TQqeHC9p8KetsN6JsjHK7UTZk7nasjjnr7XxXp9F1")

	// Find the associated token account address
	tokenAccountAddress, _, err := solana.FindAssociatedTokenAddress(owner, tokenMint)
	if err != nil {
		log.Fatalf("Failed to find associated token address, err: %v", err)
	}

	accountInfo, err := rpcClient.GetAccountInfo(context.TODO(), tokenAccountAddress)
	if err != nil || accountInfo == nil {
		log.Printf("Associated token account does not exist; creating a new one for mint: %s", tokenMint)
	}

	log.Println("accountInfo", accountInfo)

	// Add compute budget instructions
	priorityFeeValue := 0.002
	computeBudgetInstruction1 := computebudget.NewSetComputeUnitLimitInstruction(1000000).Build()
	computeBudgetInstruction2 := computebudget.NewSetComputeUnitPriceInstruction(uint64(priorityFeeValue * float64(1e9))).Build()

	log.Println("computeBudgetInstruction2", computeBudgetInstruction2)

	// Add ATA creation instruction if the ATA does not exist
	var createATAInstruction solana.Instruction
	if accountInfo == nil {
		log.Println("Creating associated token account instruction.")
		createATAInstruction = associatedtokenaccount.NewCreateInstruction(owner, owner, tokenMint).Build()
		log.Printf("createATAInstruction: %v\n", createATAInstruction)
	}

	coinData, err := getTokenData(tokenMint.String())
	if err != nil {
		log.Fatalf("Failed to get token data, err: %v", err)
	}

	log.Println("Fetched coin data", coinData)

	bondingCurve := solana.MustPublicKeyFromBase58(coinData["bonding_curve"].(string))
	associatedBondingCurve := solana.MustPublicKeyFromBase58(coinData["associated_bonding_curve"].(string))

	amount := float64(0.01) // Amount in SOL
	slippage := 0.20        // 20% slippage

	tokenOut := uint64(coinData["virtual_token_reserves"].(float64) * amount / coinData["virtual_sol_reserves"].(float64) * float64(solana.LAMPORTS_PER_SOL))
	log.Println("tokenOut", tokenOut, coinData["virtual_token_reserves"].(float64)*amount/coinData["virtual_sol_reserves"].(float64))
	amountWithSlippage := float64(solana.LAMPORTS_PER_SOL) * (amount * (1 + slippage))
	priorityFee := uint64(priorityFeeValue * 1e9) // Priority fee in lamports

	maxSolCost := uint64(amountWithSlippage) + priorityFee

	log.Println("maxSolCost", maxSolCost)

	data := append(bufferFromUInt64(16927863322537952870), bufferFromUInt64(tokenOut)...)
	data = append(data, bufferFromUInt64(maxSolCost)...)

	instruction := solana.NewInstruction(
		pumpFunProgram,
		solana.AccountMetaSlice{
			{PublicKey: global, IsSigner: false, IsWritable: false},
			{PublicKey: feeRecipient, IsSigner: false, IsWritable: true},
			{PublicKey: tokenMint, IsSigner: false, IsWritable: false},
			{PublicKey: bondingCurve, IsSigner: false, IsWritable: true},
			{PublicKey: associatedBondingCurve, IsSigner: false, IsWritable: true},
			{PublicKey: tokenAccountAddress, IsSigner: false, IsWritable: true},
			{PublicKey: owner, IsSigner: true, IsWritable: true},
			{PublicKey: solana.SystemProgramID, IsSigner: false, IsWritable: false},
			{PublicKey: solana.TokenProgramID, IsSigner: false, IsWritable: false},
			{PublicKey: solana.SysVarRentPubkey, IsSigner: false, IsWritable: false},
			{PublicKey: pumpFunAccount, IsSigner: false, IsWritable: false},
			{PublicKey: pumpFunProgram, IsSigner: false, IsWritable: false},
		},
		data,
	)

	log.Println("Prepared transaction instruction")

	// Combine all instructions
	allInstructions := []solana.Instruction{
		computeBudgetInstruction1,
		computeBudgetInstruction2,
		instruction,
	}

	if createATAInstruction != nil {
		// log.Println("createATAInstruction is not null")
		// allInstructions = append([]solana.Instruction{createATAInstruction}, allInstructions...)
		// log.Println("allInstructions", len(allInstructions))
		recentBlockhash, err := rpcClient.GetRecentBlockhash(context.TODO(), rpc.CommitmentFinalized)
		if err != nil {
			log.Fatalf("Failed to get recent blockhash, err: %v", err)
		}

		log.Println("Fetched recent blockhash:", recentBlockhash.Value.Blockhash.String())

		tx, err := solana.NewTransaction(
			[]solana.Instruction{createATAInstruction},
			recentBlockhash.Value.Blockhash,
			solana.TransactionPayer(owner),
		)
		if err != nil {
			log.Fatalf("Failed to create transaction, err: %v", err)
		}

		_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
			if owner.Equals(key) {
				return &privateKey
			}
			return nil
		})

		if err != nil {
			log.Fatalf("Failed to sign transaction, err: %v", err)
		}

		sig, err := SendAndConfirmTransaction(context.TODO(), rpcClient, wsClient, tx)
		if err != nil {
			log.Printf("Failed to send and confirm transaction : %v", err)
			return
		}

		fmt.Printf("Transaction successful with signature: %s\n", sig.String())
	}

	// Retry mechanism with exponential backoff
	maxRetries := 3
	retryDelay := 1 * time.Second

	for i := 0; i < maxRetries; i++ {
		recentBlockhash, err := rpcClient.GetRecentBlockhash(context.TODO(), rpc.CommitmentFinalized)
		if err != nil {
			log.Fatalf("Failed to get recent blockhash, err: %v", err)
		}

		tx, err := solana.NewTransaction(
			allInstructions,
			recentBlockhash.Value.Blockhash,
			solana.TransactionPayer(owner),
		)
		if err != nil {
			log.Fatalf("Failed to create transaction, err: %v", err)
		}

		_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
			if owner.Equals(key) {
				return &privateKey
			}
			return nil
		})
		if err != nil {
			log.Fatalf("Failed to sign transaction, err: %v", err)
		}

		log.Printf("Instruction data: %v", data)
		log.Printf("Instruction accounts: %+v", instruction.Accounts)
		log.Printf("Instruction program ID: %s", instruction.ProgramID().String())

		// Transaction simulation successful. Proceed with sending.
		sig, err := SendAndConfirmTransaction(context.TODO(), rpcClient, wsClient, tx)
		if err != nil {
			log.Printf("Failed to send and confirm transaction (attempt %d): %v", i+1, err)
			time.Sleep(retryDelay)
			retryDelay *= 2
			continue
		}

		fmt.Printf("Transaction successful with signature: %s\n", sig.String())
		return // Exit the loop on success
	}

	log.Fatalf("Failed to send transaction after %d retries", maxRetries)
}
