// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// clientCertWithRoleOID and clientKeyWithRoleOID are a self-signed client
// certificate carrying the Modbus Role extension "operator2", and its key, in
// PEM. They are generated when the tests start: an embedded certificate expires
// some day and takes the tests with it.
var clientCertWithRoleOID, clientKeyWithRoleOID = newRoleClientCert("operator2")

// newRoleClientCert returns a self-signed client certificate with the given
// Modbus Role, and its private key, both PEM encoded.
func newRoleClientCert(role string) (certPEM, keyPEM string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	roleValue, err := asn1.MarshalWithParams(role, "utf8")
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "TEST CLIENT CERT DO NOT USE"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions:       []pkix.Extension{{Id: modbusRoleOID, Value: roleValue}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

// TestTLSServer tests the TLS layer of the modbus server.
func TestTLSServer(t *testing.T) {
	var err error
	var server *Server
	var serverKeyPair tls.Certificate
	var client1KeyPair tls.Certificate
	var client2KeyPair tls.Certificate
	var clientCp *x509.CertPool
	var serverCp *x509.CertPool
	var th *tlsTestHandler
	var c1 *Client
	var c2 *Client
	var regs []uint16
	var coils []bool

	th = &tlsTestHandler{}

	// load server keypair (from client_tls_test.go)
	serverKeyPair, err = tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
	if err != nil {
		t.Errorf("failed to load test server key pair: %v", err)
		return
	}

	// load the first client keypair (from client_tls_test.go)
	// this client cert doesn't have any Modbus Role extension
	client1KeyPair, err = tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
	if err != nil {
		t.Errorf("failed to load test client key pair: %v", err)
		return
	}

	// load the second client keypair (defined above)
	// this client cert has an "operator2" Modbus Role extension
	client2KeyPair, err = tls.X509KeyPair(
		[]byte(clientCertWithRoleOID), []byte(clientKeyWithRoleOID))
	if err != nil {
		t.Errorf("failed to load test client key pair: %v", err)
		return
	}

	// load the server cert into the client CA cert pool to get the server cert
	// accepted by clients
	clientCp = x509.NewCertPool()
	if !clientCp.AppendCertsFromPEM([]byte(serverCert)) {
		t.Errorf("failed to load test server cert into cert pool")
	}

	// start with an empty server cert pool initially to reject the client
	// certificate
	serverCp = x509.NewCertPool()

	server, err = NewServer(&ServerConfig{
		URL:           "tcp+tls://localhost:5802",
		MaxClients:    2,
		TLSServerCert: &serverKeyPair,
		TLSClientCAs:  serverCp,
	}, th)
	if err != nil {
		t.Errorf("failed to create server: %v", err)
	}

	err = server.Start()
	if err != nil {
		t.Errorf("failed to start server: %v", err)
	}

	// create 2 modbus clients
	c1, err = New(Config{
		URL:           "tcp+tls://localhost:5802",
		TLSClientCert: &client1KeyPair,
		TLSRootCAs:    clientCp,
	})
	if err != nil {
		t.Errorf("failed to create client: %v", err)
	}
	c2, err = New(Config{
		URL:           "tcp+tls://localhost:5802",
		TLSClientCert: &client2KeyPair,
		TLSRootCAs:    clientCp,
	})
	if err != nil {
		t.Errorf("failed to create client: %v", err)
	}

	// attempt to connect and use the first client. since its cert
	// is not trusted by the server, a TLS error should occur on the first
	// request.
	err = c1.Open()
	if err != nil {
		t.Errorf("c1.Open() should have succeeded")
	}
	_, err = c1.ReadCoils(context.Background(), 1, 0, 5)
	if err == nil {
		t.Error("c1.ReadCoils(context.Background(), 1, ) should have failed")
	}
	_ = c1.Close()

	// now place both client certs in the server's authorized client list
	// to get them past the TLS client cert validation procedure
	if !serverCp.AppendCertsFromPEM([]byte(clientCert)) {
		t.Errorf("failed to load client#1 cert into cert pool")
	}
	if !serverCp.AppendCertsFromPEM([]byte(clientCertWithRoleOID)) {
		t.Errorf("failed to load client#2 cert into cert pool")
	}

	// connect both clients: should succeed
	err = c1.Open()
	if err != nil {
		t.Error("c1.Open() should have succeeded")
	}

	err = c2.Open()
	if err != nil {
		t.Error("c2.Open() should have succeeded")
	}

	// client #2 (with 'operator2' role) should have read/write access to coils while
	// client #1 (without role) should only be able to read.
	err = c1.WriteCoil(context.Background(), 1, 0, true)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("c1.WriteCoil() should have failed with %v, got: %v",
			ErrIllegalFunction, err)
	}

	coils, err = c1.ReadCoils(context.Background(), 1, 0, 5)
	if err != nil {
		t.Errorf("c1.ReadCoils() should have succeeded, got: %v", err)
	}
	if coils[0] {
		t.Errorf("coils[0] should have been false")
	}

	err = c2.WriteCoil(context.Background(), 1, 0, true)
	if err != nil {
		t.Errorf("c2.WriteCoil() should have succeeded, got: %v", err)
	}

	coils, err = c2.ReadCoils(context.Background(), 1, 0, 5)
	if err != nil {
		t.Errorf("c2.ReadCoils() should have succeeded, got: %v", err)
	}
	if !coils[0] {
		t.Errorf("coils[0] should have been true")
	}

	coils, err = c1.ReadCoils(context.Background(), 1, 0, 5)
	if err != nil {
		t.Errorf("c1.ReadCoils() should have succeeded, got: %v", err)
	}
	if !coils[0] {
		t.Errorf("coils[0] should have been true")
	}

	// client #1 should only be allowed access to holding registers of unit id #1
	// while client#2 should be allowed access to holding registers of unit ids #1 and #4
	err = c1.WriteRegister(context.Background(), 1, 2, 100)
	if err != nil {
		t.Errorf("c1.WriteRegister() should have succeeded, got: %v", err)
	}

	err = c1.WriteRegister(context.Background(), 4, 2, 200)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("c1.WriteRegister() should have failed with %v, got: %v",
			ErrIllegalFunction, err)
	}

	regs, err = c2.ReadRegisters(context.Background(), 1, 1, 2, HoldingRegister)
	if err != nil {
		t.Errorf("c2.ReadRegisters() should have succeeded, got: %v", err)
	}
	if regs[0] != 0 || regs[1] != 100 {
		t.Errorf("unexpected register values: %v", regs)
	}

	err = c2.WriteRegister(context.Background(), 4, 2, 200)
	if err != nil {
		t.Errorf("c2.WriteRegister() should have succeeded, got: %v", err)
	}

	regs, err = c2.ReadRegisters(context.Background(), 4, 1, 2, HoldingRegister)
	if err != nil {
		t.Errorf("c2.ReadRegisters() should have succeeded, got: %v", err)
	}
	if regs[0] != 0 || regs[1] != 200 {
		t.Errorf("unexpected register values: %v", regs)
	}

	// close the server and all client connections
	_ = server.Stop()

	// make sure all underlying TCP client connections have been freed
	time.Sleep(10 * time.Millisecond)
	server.lock.Lock()
	if len(server.tcpClients) != 0 {
		t.Errorf("expected 0 client connections, saw: %v", len(server.tcpClients))
	}
	server.lock.Unlock()

	// cleanup
	_ = c1.Close()
	_ = c2.Close()
}

type tlsTestHandler struct {
	coils      [10]bool
	holdingId1 [10]uint16
	holdingId4 [10]uint16
}

func (th *tlsTestHandler) HandleCoils(ctx context.Context, req *CoilsRequest) (res []bool, err error) {
	// coils access is allowed to any client with a valid cert, but
	// the "operator2" role is required to write
	if req.IsWrite && req.ClientRole != "operator2" {
		err = ErrIllegalFunction
		return
	}

	if req.Addr+req.Quantity > uint16(len(th.coils)) {
		err = ErrIllegalDataAddress
		return
	}

	for i := 0; i < int(req.Quantity); i++ {
		if req.IsWrite {
			th.coils[int(req.Addr)+i] = req.Args[i]
		}
		res = append(res, th.coils[int(req.Addr)+i])
	}

	return
}

func (th *tlsTestHandler) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) (res []bool, err error) {
	// there are no digital inputs on this device
	err = ErrIllegalDataAddress

	return
}

func (th *tlsTestHandler) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) (res []uint16, err error) {
	// gate unit id #4 behind the "operator2" role while access to unit id #1
	// is allowed to any valid cert
	switch req.UnitID {
	case 0x04:
		if req.ClientRole != "operator2" {
			err = ErrIllegalFunction
			return
		}

		if req.Addr+req.Quantity > uint16(len(th.holdingId4)) {
			err = ErrIllegalDataAddress
			return
		}

		for i := 0; i < int(req.Quantity); i++ {
			if req.IsWrite {
				th.holdingId4[int(req.Addr)+i] = req.Args[i]
			}
			res = append(res, th.holdingId4[int(req.Addr)+i])
		}
	case 0x01:
		if req.Addr+req.Quantity > uint16(len(th.holdingId1)) {
			err = ErrIllegalDataAddress
			return
		}

		for i := 0; i < int(req.Quantity); i++ {
			if req.IsWrite {
				th.holdingId1[int(req.Addr)+i] = req.Args[i]
			}
			res = append(res, th.holdingId1[int(req.Addr)+i])
		}
	default:
		err = ErrIllegalFunction
		return
	}

	return
}

func (th *tlsTestHandler) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) (res []uint16, err error) {
	// there are no inputs registers on this device
	err = ErrIllegalDataAddress

	return
}

func TestServerExtractRole(t *testing.T) {
	var ms *Server
	var pemBlock *pem.Block
	var x509Cert *x509.Certificate
	var err error
	var role string

	ms = &Server{
		logger: newLogger("test-server-role-extraction", nil),
	}

	// load a client cert without role OID
	pemBlock, _ = pem.Decode([]byte(clientCert))
	if pemBlock == nil {
		t.Errorf("failed to decode client cert")
		return
	}

	x509Cert, err = x509.ParseCertificate(pemBlock.Bytes)
	if err != nil {
		t.Errorf("failed to parse client cert: %v", err)
		return
	}

	// calling extractRole on a cert without role extension should return an
	// empty string (see R-23 of the MBAPS spec)
	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// load a certificate with a single role extension of "operator2"
	pemBlock, _ = pem.Decode([]byte(clientCertWithRoleOID))
	if pemBlock == nil {
		t.Errorf("failed to decode client cert")
		return
	}

	x509Cert, err = x509.ParseCertificate(pemBlock.Bytes)
	if err != nil {
		t.Errorf("failed to parse client cert: %v", err)
		return
	}

	role = ms.extractRole(x509Cert)
	if role != "operator2" {
		t.Errorf("role should have been 'operator2', got: '%s'", role)
	}

	// build a certificate with multiple Modbus Role extensions: they should
	// all be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x04, 0x66, 0x77, 0x67, 0x78,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 4-byte string 'fwgx'
				},
			},
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x02, 0x66, 0x67,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 2-byte string 'fwwf'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with a single Modbus Role extension of the wrong
	// type: the role should be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x13, 0x04, 0x66, 0x77, 0x67, 0x78,
					// ^ ASN1:PrintableString
					//     ^ length
					//          ^ 4-byte string 'fwgx'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with a single, short Modbus Role extension: the role
	// should be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c,
					// ^ ASN1:UTF8String
					//    ^ missing length + payload bytes
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with one bad Modbus Role extension (short) and one
	// valid: they should both be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c,
					// ^ ASN1:UTF8String
					//    ^ missing length + payload bytes
				},
			},
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x02, 0x66, 0x67,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 2-byte string 'fwwf'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with a single, valid Modbus Role extension: it should be
	// accepted
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x04, 0x66, 0x77, 0x67, 0x78,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 4-byte string 'fwgx'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "fwgx" {
		t.Errorf("role should have been 'fwgx', got: '%s'", role)
	}
}
