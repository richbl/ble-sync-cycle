package ble

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"time"

	config "github.com/richbl/go-ble-sync-cycle/internal/configuration"
	logger "github.com/richbl/go-ble-sync-cycle/internal/logging"
	speed "github.com/richbl/go-ble-sync-cycle/internal/speed"

	"tinygo.org/x/bluetooth"
)

// BLEController represents the BLE central controller component
type BLEController struct {
	bleConfig   config.BLEConfig
	speedConfig config.SpeedConfig
	bleAdapter  bluetooth.Adapter
}

var (
	// CSC speed tracking variables
	lastWheelRevs uint32
	lastWheelTime uint16
)

// NewBLEController creates a new BLE central controller for accessing a BLE peripheral
func NewBLEController(bleConfig config.BLEConfig, speedConfig config.SpeedConfig) (*BLEController, error) {

	bleAdapter := bluetooth.DefaultAdapter
	if err := bleAdapter.Enable(); err != nil {
		return nil, err
	}

	logger.Info("[BLE] Created new BLE central controller")

	return &BLEController{
		bleConfig:   bleConfig,
		speedConfig: speedConfig,
		bleAdapter:  *bleAdapter,
	}, nil

}

// GetBLECharacteristic scans for the BLE peripheral and returns CSC services/characteristics
func (m *BLEController) GetBLECharacteristic(ctx context.Context, speedController *speed.SpeedController) (*bluetooth.DeviceCharacteristic, error) {

	// Scan for BLE peripheral
	result, err := m.scanForBLEPeripheral(ctx)
	if err != nil {
		return nil, err
	}

	logger.Info("[BLE] Connecting to BLE peripheral device " + result.Address.String())

	// Connect to BLE peripheral device
	var device bluetooth.Device
	if device, err = m.bleAdapter.Connect(result.Address, bluetooth.ConnectionParams{}); err != nil {
		return nil, err
	}

	logger.Info("[BLE] BLE peripheral device connected")
	logger.Info("[BLE] Discovering CSC services " + bluetooth.New16BitUUID(0x1816).String())

	// Find CSC service and characteristic
	svc, err := device.DiscoverServices([]bluetooth.UUID{bluetooth.New16BitUUID(0x1816)})
	if err != nil {
		logger.Warn("[BLE] CSC services discovery failed: " + err.Error())
		return nil, err
	}

	logger.Info("[BLE] Found CSC service " + svc[0].UUID().String())
	logger.Info("[BLE] Discovering CSC characteristics " + bluetooth.New16BitUUID(0x2A5B).String())

	char, err := svc[0].DiscoverCharacteristics([]bluetooth.UUID{bluetooth.New16BitUUID(0x2A5B)})
	if err != nil {
		logger.Warn("[BLE] CSC characteristics discovery failed: ", err.Error())
		return nil, err
	}

	logger.Info("[BLE] Found CSC characteristic " + char[0].UUID().String())

	return &char[0], nil

}

// GetBLEUpdates enables BLE peripheral monitoring to report real-time sensor data
func (m *BLEController) GetBLEUpdates(ctx context.Context, speedController *speed.SpeedController, char *bluetooth.DeviceCharacteristic) error {

	logger.Info("[BLE] Starting real-time monitoring of BLE sensor notifications...")

	// Subscribe to live BLE sensor notifications
	if err := char.EnableNotifications(func(buf []byte) {
		speed := m.processBLESpeed(buf)
		speedController.UpdateSpeed(speed)
	}); err != nil {
		return err
	}

	<-ctx.Done()
	return nil

}

// scanForBLEPeripheral scans for the specified BLE peripheral UUID within the given timeout
func (m *BLEController) scanForBLEPeripheral(ctx context.Context) (bluetooth.ScanResult, error) {

	scanCtx, cancel := context.WithTimeout(ctx, time.Duration(m.bleConfig.ScanTimeoutSecs)*time.Second)
	defer cancel()

	found := make(chan bluetooth.ScanResult, 1)

	go func() {

		logger.Info("[BLE] Now scanning the ether for BLE peripheral UUID of " + m.bleConfig.SensorUUID + "...")

		err := m.bleAdapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {

			if result.Address.String() == m.bleConfig.SensorUUID {

				if err := m.bleAdapter.StopScan(); err != nil {
					logger.Error("[BLE] Failed to stop scan: " + err.Error())
				}

				found <- result
			}

		})

		if err != nil {
			logger.Error("[BLE] Scan error: " + err.Error())
		}

	}()

	// Wait for the scan to complete or context cancellation
	select {
	case result := <-found:
		logger.Info("[BLE] Found BLE peripheral " + result.Address.String())
		return result, nil
	case <-scanCtx.Done():
		if err := m.bleAdapter.StopScan(); err != nil {
			logger.Info("[BLE] Failed to stop scan: " + err.Error())
		}
		return bluetooth.ScanResult{}, errors.New("scanning time limit reached")
	}

}

// processBLESpeed processes raw BLE CSC speed data and returns the adjusted current speed
func (m *BLEController) processBLESpeed(data []byte) float64 {
	if len(data) < 1 {
		return 0.0
	}

	logger.Info("[SPEED] Processing speed data from BLE peripheral...")

	flags := data[0]
	hasWheelRev := flags&0x01 != 0

	if !hasWheelRev || len(data) < 7 {
		return 0.0
	}

	wheelRevs := binary.LittleEndian.Uint32(data[1:])
	wheelEventTime := binary.LittleEndian.Uint16(data[5:])

	if lastWheelTime == 0 {
		lastWheelRevs = wheelRevs
		lastWheelTime = wheelEventTime
		return 0.0
	}

	timeDiff := uint16(wheelEventTime - lastWheelTime)
	if timeDiff == 0 {
		return 0.0
	}

	revDiff := int32(wheelRevs - lastWheelRevs)
	speedConversion := 3.6
	if m.speedConfig.SpeedUnits == "mph" {
		speedConversion = 2.23694
	}

	speed := float64(revDiff) * float64(m.speedConfig.WheelCircumferenceMM) * speedConversion / float64(timeDiff)

	logger.Info("[SPEED] BLE sensor speed: " + strconv.FormatFloat(math.Round(speed*100)/100, 'f', 2, 64) + " " + m.speedConfig.SpeedUnits)

	lastWheelRevs = wheelRevs
	lastWheelTime = wheelEventTime

	return speed
}
