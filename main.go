package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// The command to start Anvil
const anvilCommand string = "anvil"

// The Anvil ipc path
const ipcPath string = "/tmp/anvil.ipc"

// The message at which Anvil starts up
const startupMessage string = "Listening on"

func main() {
	// Setup context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Anvil executable
	anvil := exec.Command(anvilCommand, "--ipc")

	// Output pipe of the Anvil process
	stdout, err := anvil.StdoutPipe()
	if err != nil {
		panic(err)
	}

	// Start the Anvil process
	if err := anvil.Start(); err != nil {
		panic(err)
	}

	// Notifies that the anvil process has started
	start := make(chan struct{})

	// Print the output of the Anvil process
	go func() {
		var started bool
		scanner := bufio.NewScanner(stdout)

		for scanner.Scan() {
			m := scanner.Text()
			fmt.Println(m)

			// Notify the start channel that Anvil has started
			if strings.HasPrefix(m, startupMessage) && !started {
				close(start)
				started = true
			}
		}
	}()

	// Wait for the Anvil process to exit
	go func() {
		if err := anvil.Wait(); err != nil {
			panic(err)
		}
	}()

	// Kill the Anvil process on exit
	defer func() {
		if err := anvil.Process.Kill(); err != nil {
			panic(err)
		}
	}()

	// Wait for the Anvil process to start
	select {
	case <-start:
		fmt.Println("Started the Anvil process")
	case <-ctx.Done():
		return
	}

	// Connect to the Anvil ipc with a timeout
	dCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := rpc.DialContext(dCtx, ipcPath)
	if err != nil {
		panic(err)
	}
	client := ethclient.NewClient(c)
	defer client.Close()

	fmt.Println("Connected to the Anvil process")

	// Channels for communicating with the snapshot capture routine
	snapshotCh := make(chan uint64, 1)
	savedSnapshotCh := make(chan struct{})

	// Start the snapshot capture goroutine to capture and save snapshots to disk
	go func() {
		for {
			blockNumber := <-snapshotCh

			var result string
			err = c.CallContext(ctx, &result, "anvil_dumpState")
			if err != nil {
				panic(err)
			}

			fmt.Printf("Captured snapshot at block %d\n", blockNumber)
			fmt.Println(result)

			savedSnapshotCh <- struct{}{}
		}
	}()

	// Capture a snapshot on startup
	blockNumber, err := client.BlockNumber(ctx)
	if err != nil {
		panic(err)
	}
	snapshotCh <- blockNumber
	<-savedSnapshotCh

	// Subscribe to new blocks
	newHeadCh := make(chan *types.Header)
	subscription, err := client.SubscribeNewHead(ctx, newHeadCh)
	if err != nil {
		panic(err)
	}
	defer subscription.Unsubscribe()

	fmt.Println("Subscribed to new blocks")

	var (
		snapshot        bool
		pendingSnapshot uint64
	)

	for {
		select {
		case <-savedSnapshotCh:
			if pendingSnapshot != 0 {
				snapshotCh <- pendingSnapshot
				pendingSnapshot = 0
			} else {
				snapshot = false
			}
		case header := <-newHeadCh:
			if snapshot {
				pendingSnapshot = header.Number.Uint64()
			} else {
				snapshotCh <- header.Number.Uint64()
				snapshot = true
				pendingSnapshot = 0
			}
		case err := <-subscription.Err():
			fmt.Printf("Subscription err: %v\n", err)
		case <-ctx.Done():
			return
		}
	}
}
