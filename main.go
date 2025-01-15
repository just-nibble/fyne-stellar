package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/stellar/go/clients/horizonclient"
	"github.com/stellar/go/keypair"
	"github.com/stellar/go/network"
	"github.com/stellar/go/txnbuild"
)

type Wallet struct {
	PublicKey string `json:"public_key"`
	SecretKey string `json:"secret_key"`
	Balance   string `json:"balance"`
	Network   string `json:"network"` // "public" or "testnet"
}

const walletFile = "stellar_wallet.json"

var (
	wallet Wallet
	client *horizonclient.Client
)

// Initialize Horizon client based on network
func initializeClient(network string) {
	if network == "testnet" {
		client = horizonclient.DefaultTestNetClient
	} else {
		client = horizonclient.DefaultPublicNetClient
	}
}

// Load or create new wallet
func loadWallet() error {
	data, err := os.ReadFile(walletFile)
	if err != nil {
		// Create new wallet if file doesn't exist
		kp, err := keypair.Random()
		if err != nil {
			return err
		}

		wallet = Wallet{
			PublicKey: kp.Address(),
			SecretKey: kp.Seed(),
			Network:   "testnet",
			Balance:   "0",
		}

		fundAccount(kp.Address())
		initializeClient(wallet.Network)
		return saveWallet()
	}

	err = json.Unmarshal(data, &wallet)
	if err != nil {
		return err
	}

	initializeClient(wallet.Network)
	return nil
}

func saveWallet() error {
	data, err := json.MarshalIndent(wallet, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(walletFile, data, 0600)
}

func fundAccount(address string) error {
	resp, err := http.Get("https://friendbot.stellar.org/?addr=" + address)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return nil

}

func updateBalance() string {
	account, err := client.AccountDetail(horizonclient.AccountRequest{
		AccountID: wallet.PublicKey,
	})
	if err != nil {
		return "Account not found (unfunded)"
	}

	for _, balance := range account.Balances {
		if balance.Asset.Type == "native" {
			wallet.Balance = balance.Balance
			saveWallet()
			return fmt.Sprintf("Balance: %s XLM", balance.Balance)
		}
	}
	return "No XLM balance found"
}

func showSendDialog(balanceLabel *widget.Label) {
	window := fyne.CurrentApp().Driver().AllWindows()[0]

	recipientEntry := widget.NewEntry()
	amountEntry := widget.NewEntry()
	memoEntry := widget.NewEntry()

	recipientEntry.SetPlaceHolder("Recipient address")
	amountEntry.SetPlaceHolder("Amount (XLM)")
	memoEntry.SetPlaceHolder("Memo (optional)")

	items := []*widget.FormItem{
		widget.NewFormItem("Recipient", recipientEntry),
		widget.NewFormItem("Amount", amountEntry),
		widget.NewFormItem("Memo", memoEntry),
	}

	dialog.ShowForm("Send XLM", "Send", "Cancel", items, func(submit bool) {
		if submit {
			sendXLM(recipientEntry.Text, amountEntry.Text, memoEntry.Text, balanceLabel)
		}
	}, window)
}

func createMainUI() fyne.CanvasObject {
	// Balance display
	balanceLabel := widget.NewLabel(updateBalance())

	// Network selection
	networkSelect := widget.NewSelect([]string{"testnet", "public"}, func(network string) {
		wallet.Network = network
		initializeClient(network)
		saveWallet()
		balanceLabel.SetText(updateBalance())
	})
	networkSelect.SetSelected(wallet.Network)

	// Address display and copy button
	addressEntry := widget.NewEntry()
	addressEntry.SetText(wallet.PublicKey)
	addressEntry.Disable()

	copyButton := widget.NewButton("Copy Address", func() {
		addressEntry.SetText(wallet.PublicKey)
		window := fyne.CurrentApp().Driver().AllWindows()[0]
		dialog.ShowInformation("Success", "Address copied to clipboard!", window)
	})

	// Send XLM button
	sendButton := widget.NewButton("Send XLM", func() {
		showSendDialog(balanceLabel)
	})

	// Transaction history button
	historyButton := widget.NewButton("Transaction History", func() {
		showTransactionHistory()
	})

	return container.NewVBox(
		widget.NewLabel("Stellar Wallet"),
		container.NewHBox(widget.NewLabel("Network:"), networkSelect),
		balanceLabel,
		container.NewHBox(addressEntry, copyButton),
		sendButton,
		historyButton,
	)
}

func sendXLM(recipient, amount, memo string, balanceLabel *widget.Label) {
	window := fyne.CurrentApp().Driver().AllWindows()[0]

	// Input validation
	if strings.TrimSpace(recipient) == "" || strings.TrimSpace(amount) == "" {
		dialog.ShowError(fmt.Errorf("recipient and amount are required"), window)
		return
	}

	// Make sure destination account exists
	destAccountRequest := horizonclient.AccountRequest{AccountID: recipient}
	_, err := client.AccountDetail(destAccountRequest)
	if err != nil {
		dialog.ShowError(fmt.Errorf("destination account does not exist: %v", err), window)
		return
	}

	// Load the source account
	sourceKP := keypair.MustParseFull(wallet.SecretKey)
	sourceAccountRequest := horizonclient.AccountRequest{AccountID: sourceKP.Address()}
	sourceAccount, err := client.AccountDetail(sourceAccountRequest)
	if err != nil {
		dialog.ShowError(fmt.Errorf("source account does not exist: %v", err), window)
		return
	}

	// Validate amount
	_, err = strconv.ParseFloat(amount, 64)
	if err != nil {
		dialog.ShowError(fmt.Errorf("invalid amount: %v", err), window)
		return
	}

	// Build transaction
	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &sourceAccount,
			IncrementSequenceNum: true,
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions: txnbuild.Preconditions{
				TimeBounds: txnbuild.NewTimeout(300),
			},
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					Destination: recipient,
					Amount:      amount,
					Asset:       txnbuild.NativeAsset{},
				},
			},
			Memo: txnbuild.MemoText(memo),
		},
	)
	if err != nil {
		log.Println(err)
		dialog.ShowError(fmt.Errorf("error building transaction: %v", err), window)
		return
	}

	// Sign the transaction
	tx, err = tx.Sign(network.TestNetworkPassphrase, sourceKP)
	if err != nil {
		log.Println(err)
		dialog.ShowError(fmt.Errorf("error signing transaction: %v", err), window)
		return
	}

	// Submit transaction
	resp, err := client.SubmitTransaction(tx)
	if err != nil {
		log.Println(err)
		dialog.ShowError(fmt.Errorf("error submitting transaction: %v", err), window)
		return
	}

	dialog.ShowInformation("Success", fmt.Sprintf("Transaction successful! Hash: %s", resp.Hash), window)
	balanceLabel.SetText(updateBalance())
}

func showTransactionHistory() {
	window := fyne.CurrentApp().Driver().AllWindows()[0]

	// Get transactions
	transactions, err := client.Transactions(horizonclient.TransactionRequest{
		ForAccount: wallet.PublicKey,
		Limit:      20,
	})
	if err != nil {
		dialog.ShowError(fmt.Errorf("error loading transactions: %v", err), window)
		return
	}

	// Create list of transactions
	var items []string
	for _, tx := range transactions.Embedded.Records {
		items = append(items, fmt.Sprintf("Hash: %s\nCreated: %s\nFee: %d XLM",
			tx.Hash, tx.LedgerCloseTime, tx.FeeCharged))
	}

	list := widget.NewTextGrid()
	list.SetText(strings.Join(items, "\n\n"))

	dialog.ShowCustom("Transaction History", "Close",
		container.NewScroll(list), window)
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Stellar Wallet")

	if err := loadWallet(); err != nil {
		log.Fatal(err)
	}

	myWindow.SetContent(createMainUI())
	myWindow.Resize(fyne.NewSize(360, 640))
	myWindow.ShowAndRun()
}
