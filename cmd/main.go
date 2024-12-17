package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	ble "github.com/richbl/go-ble-sync-cycle/internal/ble"
	config "github.com/richbl/go-ble-sync-cycle/internal/configuration"
	logger "github.com/richbl/go-ble-sync-cycle/internal/logging"
	speed "github.com/richbl/go-ble-sync-cycle/internal/speed"
	video "github.com/richbl/go-ble-sync-cycle/internal/video-player"

	"tinygo.org/x/bluetooth"
)

// appControllers holds the main application controllers
type appControllers struct {
	speedController *speed.SpeedController
	videoPlayer     *video.PlaybackController
	bleController   *ble.BLEController
}

func main() {
	log.Println("Starting BLE Sync Cycle 0.6.2")

	// Load configuration
	cfg, err := config.LoadFile("config.toml")
	if err != nil {
		log.Fatal("[FATAL]: failed to load TOML configuration: " + err.Error())
	}

	// Initialize logger
	if _, err := logger.Initialize(cfg.App.LogLevel); err != nil {
		log.Printf("[WARN]: logger initialization warning: %v", err)
	}

	// Create contexts for managing goroutines and cancellations
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Create component controllers
	controllers, componentType, err := setupAppControllers(*cfg)
	if err != nil {
		logger.Fatal(componentType, "failed to create controllers: "+err.Error())
	}

	// Run the application
	if componentType, err := startAppControllers(rootCtx, controllers); err != nil {
		logger.Error(componentType, err.Error())
	}

	// Shutdown the application... buh bye!
	logger.Info(logger.APP, "application shutdown complete... goodbye!")
}

// setupAppControllers creates and initializes the application controllers
func setupAppControllers(cfg config.Config) (appControllers, logger.ComponentType, error) {

	// Create speed  and video controllers
	speedController := speed.NewSpeedController(cfg.Speed.SmoothingWindow)
	videoPlayer, err := video.NewPlaybackController(cfg.Video, cfg.Speed)
	if err != nil {
		return appControllers{}, logger.VIDEO, errors.New("failed to create video player: " + err.Error())
	}

	// Create BLE controller
	bleController, err := ble.NewBLEController(cfg.BLE, cfg.Speed)
	if err != nil {
		return appControllers{}, logger.BLE, errors.New("failed to create BLE controller: " + err.Error())
	}

	return appControllers{
		speedController: speedController,
		videoPlayer:     videoPlayer,
		bleController:   bleController,
	}, logger.APP, nil
}

// startAppControllers is responsible for starting and managing the component controllers
func startAppControllers(ctx context.Context, controllers appControllers) (logger.ComponentType, error) {
	
	// componentErr holds the error type and component type used for logging
	type componentErr struct {
		componentType logger.ComponentType
		err          error
	}

	// Create shutdown signal
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Scan for BLE peripheral of interest
	bleSpeedCharacter, err := scanForBLESpeedCharacteristic(ctx, controllers)
	if err != nil {
		return logger.BLE, errors.New("BLE peripheral scan failed: " + err.Error())
	}

	// Start component controllers concurrently
	errs := make(chan componentErr, 1)

	// Monitor BLE speed
	go func() {
		if err := monitorBLESpeed(ctx, controllers, bleSpeedCharacter); err != nil {
			errs <- componentErr{logger.BLE, err}
			return
		}
		errs <- componentErr{logger.BLE, nil}
	}()

	// Play video
	go func() {
		if err := playVideo(ctx, controllers); err != nil {
			errs <- componentErr{logger.VIDEO, err}
			return
		}
		errs <- componentErr{logger.VIDEO, nil}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		return logger.APP, ctx.Err()
	case compErr := <-errs:
		if compErr.err != nil {
			return compErr.componentType, compErr.err
		}
	}

	return logger.APP, nil
}

// scanForBLESpeedCharacteristic scans for the BLE CSC speed characteristic
func scanForBLESpeedCharacteristic(ctx context.Context, controllers appControllers) (*bluetooth.DeviceCharacteristic, error) {

	// create a channel to receive the characteristic
	results := make(chan *bluetooth.DeviceCharacteristic, 1)
	errChan := make(chan error, 1)

	// Scan for the BLE CSC speed characteristic
	go func() {
		characteristic, err := controllers.bleController.GetBLECharacteristic(ctx, controllers.speedController)
		if err != nil {
			errChan <- err
			return
		}

		// Return the characteristic
		results <- characteristic
	}()

	// Wait for the characteristic or an error
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errChan:
		return nil, err
	case characteristic := <-results:
		return characteristic, nil
	}
}

// monitorBLESpeed monitors the BLE speed characteristic
func monitorBLESpeed(ctx context.Context, controllers appControllers, bleSpeedCharacter *bluetooth.DeviceCharacteristic) error {
	return controllers.bleController.GetBLEUpdates(ctx, controllers.speedController, bleSpeedCharacter)
}

// playVideo starts the video player
func playVideo(ctx context.Context, controllers appControllers) error {
	return controllers.videoPlayer.Start(ctx, controllers.speedController)
}
