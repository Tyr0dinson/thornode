package zec

// #include <zec.h>
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"unsafe"
)

type RustBuffer = C.RustBuffer

type RustBufferI interface {
	AsReader() *bytes.Reader
	Free()
	ToGoBytes() []byte
	Data() unsafe.Pointer
	Len() int
	Capacity() int
}

func RustBufferFromExternal(b RustBufferI) RustBuffer {
	return RustBuffer{
		capacity: C.int(b.Capacity()),
		len:      C.int(b.Len()),
		data:     (*C.uchar)(b.Data()),
	}
}

func (cb RustBuffer) Capacity() int {
	return int(cb.capacity)
}

func (cb RustBuffer) Len() int {
	return int(cb.len)
}

func (cb RustBuffer) Data() unsafe.Pointer {
	return unsafe.Pointer(cb.data)
}

func (cb RustBuffer) AsReader() *bytes.Reader {
	b := unsafe.Slice((*byte)(cb.data), C.int(cb.len))
	return bytes.NewReader(b)
}

func (cb RustBuffer) Free() {
	rustCall(func(status *C.RustCallStatus) bool {
		C.ffi_zec_rustbuffer_free(cb, status)
		return false
	})
}

func (cb RustBuffer) ToGoBytes() []byte {
	return C.GoBytes(unsafe.Pointer(cb.data), C.int(cb.len))
}

func stringToRustBuffer(str string) RustBuffer {
	return bytesToRustBuffer([]byte(str))
}

func bytesToRustBuffer(b []byte) RustBuffer {
	if len(b) == 0 {
		return RustBuffer{}
	}
	// We can pass the pointer along here, as it is pinned
	// for the duration of this call
	foreign := C.ForeignBytes{
		len:  C.int(len(b)),
		data: (*C.uchar)(unsafe.Pointer(&b[0])),
	}

	return rustCall(func(status *C.RustCallStatus) RustBuffer {
		return C.ffi_zec_rustbuffer_from_bytes(foreign, status)
	})
}

type BufLifter[GoType any] interface {
	Lift(value RustBufferI) GoType
}

type BufLowerer[GoType any] interface {
	Lower(value GoType) RustBuffer
}

type FfiConverter[GoType any, FfiType any] interface {
	Lift(value FfiType) GoType
	Lower(value GoType) FfiType
}

type BufReader[GoType any] interface {
	Read(reader io.Reader) GoType
}

type BufWriter[GoType any] interface {
	Write(writer io.Writer, value GoType)
}

type FfiRustBufConverter[GoType any, FfiType any] interface {
	FfiConverter[GoType, FfiType]
	BufReader[GoType]
}

func LowerIntoRustBuffer[GoType any](bufWriter BufWriter[GoType], value GoType) RustBuffer {
	// This might be not the most efficient way but it does not require knowing allocation size
	// beforehand
	var buffer bytes.Buffer
	bufWriter.Write(&buffer, value)

	bytes, err := io.ReadAll(&buffer)
	if err != nil {
		panic(fmt.Errorf("reading written data: %w", err))
	}
	return bytesToRustBuffer(bytes)
}

func LiftFromRustBuffer[GoType any](bufReader BufReader[GoType], rbuf RustBufferI) GoType {
	defer rbuf.Free()
	reader := rbuf.AsReader()
	item := bufReader.Read(reader)
	if reader.Len() > 0 {
		// TODO: Remove this
		leftover, _ := io.ReadAll(reader)
		panic(fmt.Errorf("Junk remaining in buffer after lifting: %s", string(leftover)))
	}
	return item
}

func rustCallWithError[U any](converter BufLifter[error], callback func(*C.RustCallStatus) U) (U, error) {
	var status C.RustCallStatus
	returnValue := callback(&status)
	err := checkCallStatus(converter, status)

	return returnValue, err
}

func checkCallStatus(converter BufLifter[error], status C.RustCallStatus) error {
	switch status.code {
	case 0:
		return nil
	case 1:
		return converter.Lift(status.errorBuf)
	case 2:
		// when the rust code sees a panic, it tries to construct a rustbuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(status.errorBuf)))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		return fmt.Errorf("unknown status code: %d", status.code)
	}
}

func checkCallStatusUnknown(status C.RustCallStatus) error {
	switch status.code {
	case 0:
		return nil
	case 1:
		panic(fmt.Errorf("function not returning an error returned an error"))
	case 2:
		// when the rust code sees a panic, it tries to construct a rustbuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(status.errorBuf)))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		return fmt.Errorf("unknown status code: %d", status.code)
	}
}

func rustCall[U any](callback func(*C.RustCallStatus) U) U {
	returnValue, err := rustCallWithError(nil, callback)
	if err != nil {
		panic(err)
	}
	return returnValue
}

func writeInt8(writer io.Writer, value int8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint8(writer io.Writer, value uint8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt16(writer io.Writer, value int16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint16(writer io.Writer, value uint16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt32(writer io.Writer, value int32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint32(writer io.Writer, value uint32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt64(writer io.Writer, value int64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint64(writer io.Writer, value uint64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat32(writer io.Writer, value float32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat64(writer io.Writer, value float64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func readInt8(reader io.Reader) int8 {
	var result int8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint8(reader io.Reader) uint8 {
	var result uint8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt16(reader io.Reader) int16 {
	var result int16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint16(reader io.Reader) uint16 {
	var result uint16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt32(reader io.Reader) int32 {
	var result int32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint32(reader io.Reader) uint32 {
	var result uint32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt64(reader io.Reader) int64 {
	var result int64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint64(reader io.Reader) uint64 {
	var result uint64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat32(reader io.Reader) float32 {
	var result float32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat64(reader io.Reader) float64 {
	var result float64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func init() {
	uniffiCheckChecksums()
}

func uniffiCheckChecksums() {
	// Get the bindings contract version from our ComponentInterface
	bindingsContractVersion := 24
	// Get the scaffolding contract version by calling the into the dylib
	scaffoldingContractVersion := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint32_t {
		return C.ffi_zec_uniffi_contract_version(uniffiStatus)
	})
	if bindingsContractVersion != int(scaffoldingContractVersion) {
		// If this happens try cleaning and rebuilding your project
		panic("zec: UniFFI contract version mismatch")
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_apply_signatures(uniffiStatus)
		})
		if checksum != 60547 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_apply_signatures: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_build_orchard_witness_from_blocks(uniffiStatus)
		})
		if checksum != 16891 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_build_orchard_witness_from_blocks: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_build_ptx(uniffiStatus)
		})
		if checksum != 317 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_build_ptx: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_build_witness_from_blocks(uniffiStatus)
		})
		if checksum != 25321 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_build_witness_from_blocks: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_compute_txid(uniffiStatus)
		})
		if checksum != 4560 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_compute_txid: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_decrypt_notes_in_tx(uniffiStatus)
		})
		if checksum != 49416 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_decrypt_notes_in_tx: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_decrypt_orchard_notes_in_tx(uniffiStatus)
		})
		if checksum != 40570 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_decrypt_orchard_notes_in_tx: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_derive_account_from_mnemonic(uniffiStatus)
		})
		if checksum != 34490 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_derive_account_from_mnemonic: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_get_ovk(uniffiStatus)
		})
		if checksum != 61727 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_get_ovk: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_init_zec(uniffiStatus)
		})
		if checksum != 23758 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_init_zec: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_is_transparent_address(uniffiStatus)
		})
		if checksum != 33395 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_is_transparent_address: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_send_from_orchard_to_transparent(uniffiStatus)
		})
		if checksum != 14380 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_send_from_orchard_to_transparent: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_send_from_sapling_to_transparent(uniffiStatus)
		})
		if checksum != 26088 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_send_from_sapling_to_transparent: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_validate_address_v1(uniffiStatus)
		})
		if checksum != 40566 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_validate_address_v1: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_validate_address_v124(uniffiStatus)
		})
		if checksum != 61649 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_validate_address_v124: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_zec_checksum_func_validate_shielded_source(uniffiStatus)
		})
		if checksum != 3572 {
			// If this happens try cleaning and rebuilding your project
			panic("zec: uniffi_zec_checksum_func_validate_shielded_source: UniFFI API checksum mismatch")
		}
	}
}

type FfiConverterUint32 struct{}

var FfiConverterUint32INSTANCE = FfiConverterUint32{}

func (FfiConverterUint32) Lower(value uint32) C.uint32_t {
	return C.uint32_t(value)
}

func (FfiConverterUint32) Write(writer io.Writer, value uint32) {
	writeUint32(writer, value)
}

func (FfiConverterUint32) Lift(value C.uint32_t) uint32 {
	return uint32(value)
}

func (FfiConverterUint32) Read(reader io.Reader) uint32 {
	return readUint32(reader)
}

type FfiDestroyerUint32 struct{}

func (FfiDestroyerUint32) Destroy(_ uint32) {}

type FfiConverterUint64 struct{}

var FfiConverterUint64INSTANCE = FfiConverterUint64{}

func (FfiConverterUint64) Lower(value uint64) C.uint64_t {
	return C.uint64_t(value)
}

func (FfiConverterUint64) Write(writer io.Writer, value uint64) {
	writeUint64(writer, value)
}

func (FfiConverterUint64) Lift(value C.uint64_t) uint64 {
	return uint64(value)
}

func (FfiConverterUint64) Read(reader io.Reader) uint64 {
	return readUint64(reader)
}

type FfiDestroyerUint64 struct{}

func (FfiDestroyerUint64) Destroy(_ uint64) {}

type FfiConverterBool struct{}

var FfiConverterBoolINSTANCE = FfiConverterBool{}

func (FfiConverterBool) Lower(value bool) C.int8_t {
	if value {
		return C.int8_t(1)
	}
	return C.int8_t(0)
}

func (FfiConverterBool) Write(writer io.Writer, value bool) {
	if value {
		writeInt8(writer, 1)
	} else {
		writeInt8(writer, 0)
	}
}

func (FfiConverterBool) Lift(value C.int8_t) bool {
	return value != 0
}

func (FfiConverterBool) Read(reader io.Reader) bool {
	return readInt8(reader) != 0
}

type FfiDestroyerBool struct{}

func (FfiDestroyerBool) Destroy(_ bool) {}

type FfiConverterString struct{}

var FfiConverterStringINSTANCE = FfiConverterString{}

func (FfiConverterString) Lift(rb RustBufferI) string {
	defer rb.Free()
	reader := rb.AsReader()
	b, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Errorf("reading reader: %w", err))
	}
	return string(b)
}

func (FfiConverterString) Read(reader io.Reader) string {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading string, expected %d, read %d", length, read_length))
	}
	return string(buffer)
}

func (FfiConverterString) Lower(value string) RustBuffer {
	return stringToRustBuffer(value)
}

func (FfiConverterString) Write(writer io.Writer, value string) {
	if len(value) > math.MaxInt32 {
		panic("String is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := io.WriteString(writer, value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing string, expected %d, written %d", len(value), write_length))
	}
}

type FfiDestroyerString struct{}

func (FfiDestroyerString) Destroy(_ string) {}

type FfiConverterBytes struct{}

var FfiConverterBytesINSTANCE = FfiConverterBytes{}

func (c FfiConverterBytes) Lower(value []byte) RustBuffer {
	return LowerIntoRustBuffer[[]byte](c, value)
}

func (c FfiConverterBytes) Write(writer io.Writer, value []byte) {
	if len(value) > math.MaxInt32 {
		panic("[]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := writer.Write(value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing []byte, expected %d, written %d", len(value), write_length))
	}
}

func (c FfiConverterBytes) Lift(rb RustBufferI) []byte {
	return LiftFromRustBuffer[[]byte](c, rb)
}

func (c FfiConverterBytes) Read(reader io.Reader) []byte {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading []byte, expected %d, read %d", length, read_length))
	}
	return buffer
}

type FfiDestroyerBytes struct{}

func (FfiDestroyerBytes) Destroy(_ []byte) {}

type DecryptedNote struct {
	Diversifier []byte
	Value       uint64
	Rseed       []byte
	Zip212      bool
}

func (r *DecryptedNote) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Diversifier)
	FfiDestroyerUint64{}.Destroy(r.Value)
	FfiDestroyerBytes{}.Destroy(r.Rseed)
	FfiDestroyerBool{}.Destroy(r.Zip212)
}

type FfiConverterTypeDecryptedNote struct{}

var FfiConverterTypeDecryptedNoteINSTANCE = FfiConverterTypeDecryptedNote{}

func (c FfiConverterTypeDecryptedNote) Lift(rb RustBufferI) DecryptedNote {
	return LiftFromRustBuffer[DecryptedNote](c, rb)
}

func (c FfiConverterTypeDecryptedNote) Read(reader io.Reader) DecryptedNote {
	return DecryptedNote{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeDecryptedNote) Lower(value DecryptedNote) RustBuffer {
	return LowerIntoRustBuffer[DecryptedNote](c, value)
}

func (c FfiConverterTypeDecryptedNote) Write(writer io.Writer, value DecryptedNote) {
	FfiConverterBytesINSTANCE.Write(writer, value.Diversifier)
	FfiConverterUint64INSTANCE.Write(writer, value.Value)
	FfiConverterBytesINSTANCE.Write(writer, value.Rseed)
	FfiConverterBoolINSTANCE.Write(writer, value.Zip212)
}

type FfiDestroyerTypeDecryptedNote struct{}

func (_ FfiDestroyerTypeDecryptedNote) Destroy(value DecryptedNote) {
	value.Destroy()
}

type OrchardDecryptedNote struct {
	Diversifier []byte
	Value       uint64
	Rseed       []byte
	Rho         []byte
	Cmx         []byte
	AddrBytes   []byte
}

func (r *OrchardDecryptedNote) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Diversifier)
	FfiDestroyerUint64{}.Destroy(r.Value)
	FfiDestroyerBytes{}.Destroy(r.Rseed)
	FfiDestroyerBytes{}.Destroy(r.Rho)
	FfiDestroyerBytes{}.Destroy(r.Cmx)
	FfiDestroyerBytes{}.Destroy(r.AddrBytes)
}

type FfiConverterTypeOrchardDecryptedNote struct{}

var FfiConverterTypeOrchardDecryptedNoteINSTANCE = FfiConverterTypeOrchardDecryptedNote{}

func (c FfiConverterTypeOrchardDecryptedNote) Lift(rb RustBufferI) OrchardDecryptedNote {
	return LiftFromRustBuffer[OrchardDecryptedNote](c, rb)
}

func (c FfiConverterTypeOrchardDecryptedNote) Read(reader io.Reader) OrchardDecryptedNote {
	return OrchardDecryptedNote{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeOrchardDecryptedNote) Lower(value OrchardDecryptedNote) RustBuffer {
	return LowerIntoRustBuffer[OrchardDecryptedNote](c, value)
}

func (c FfiConverterTypeOrchardDecryptedNote) Write(writer io.Writer, value OrchardDecryptedNote) {
	FfiConverterBytesINSTANCE.Write(writer, value.Diversifier)
	FfiConverterUint64INSTANCE.Write(writer, value.Value)
	FfiConverterBytesINSTANCE.Write(writer, value.Rseed)
	FfiConverterBytesINSTANCE.Write(writer, value.Rho)
	FfiConverterBytesINSTANCE.Write(writer, value.Cmx)
	FfiConverterBytesINSTANCE.Write(writer, value.AddrBytes)
}

type FfiDestroyerTypeOrchardDecryptedNote struct{}

func (_ FfiDestroyerTypeOrchardDecryptedNote) Destroy(value OrchardDecryptedNote) {
	value.Destroy()
}

type OrchardSpendableNote struct {
	Diversifier []byte
	Value       uint64
	Rseed       []byte
	Rho         []byte
	Witness     []byte
	Cmx         []byte
	AddrBytes   []byte
}

func (r *OrchardSpendableNote) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Diversifier)
	FfiDestroyerUint64{}.Destroy(r.Value)
	FfiDestroyerBytes{}.Destroy(r.Rseed)
	FfiDestroyerBytes{}.Destroy(r.Rho)
	FfiDestroyerBytes{}.Destroy(r.Witness)
	FfiDestroyerBytes{}.Destroy(r.Cmx)
	FfiDestroyerBytes{}.Destroy(r.AddrBytes)
}

type FfiConverterTypeOrchardSpendableNote struct{}

var FfiConverterTypeOrchardSpendableNoteINSTANCE = FfiConverterTypeOrchardSpendableNote{}

func (c FfiConverterTypeOrchardSpendableNote) Lift(rb RustBufferI) OrchardSpendableNote {
	return LiftFromRustBuffer[OrchardSpendableNote](c, rb)
}

func (c FfiConverterTypeOrchardSpendableNote) Read(reader io.Reader) OrchardSpendableNote {
	return OrchardSpendableNote{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeOrchardSpendableNote) Lower(value OrchardSpendableNote) RustBuffer {
	return LowerIntoRustBuffer[OrchardSpendableNote](c, value)
}

func (c FfiConverterTypeOrchardSpendableNote) Write(writer io.Writer, value OrchardSpendableNote) {
	FfiConverterBytesINSTANCE.Write(writer, value.Diversifier)
	FfiConverterUint64INSTANCE.Write(writer, value.Value)
	FfiConverterBytesINSTANCE.Write(writer, value.Rseed)
	FfiConverterBytesINSTANCE.Write(writer, value.Rho)
	FfiConverterBytesINSTANCE.Write(writer, value.Witness)
	FfiConverterBytesINSTANCE.Write(writer, value.Cmx)
	FfiConverterBytesINSTANCE.Write(writer, value.AddrBytes)
}

type FfiDestroyerTypeOrchardSpendableNote struct{}

func (_ FfiDestroyerTypeOrchardSpendableNote) Destroy(value OrchardSpendableNote) {
	value.Destroy()
}

type OrchardWitnessParts struct {
	WitnessBytes []byte
	AnchorBytes  []byte
}

func (r *OrchardWitnessParts) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.WitnessBytes)
	FfiDestroyerBytes{}.Destroy(r.AnchorBytes)
}

type FfiConverterTypeOrchardWitnessParts struct{}

var FfiConverterTypeOrchardWitnessPartsINSTANCE = FfiConverterTypeOrchardWitnessParts{}

func (c FfiConverterTypeOrchardWitnessParts) Lift(rb RustBufferI) OrchardWitnessParts {
	return LiftFromRustBuffer[OrchardWitnessParts](c, rb)
}

func (c FfiConverterTypeOrchardWitnessParts) Read(reader io.Reader) OrchardWitnessParts {
	return OrchardWitnessParts{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeOrchardWitnessParts) Lower(value OrchardWitnessParts) RustBuffer {
	return LowerIntoRustBuffer[OrchardWitnessParts](c, value)
}

func (c FfiConverterTypeOrchardWitnessParts) Write(writer io.Writer, value OrchardWitnessParts) {
	FfiConverterBytesINSTANCE.Write(writer, value.WitnessBytes)
	FfiConverterBytesINSTANCE.Write(writer, value.AnchorBytes)
}

type FfiDestroyerTypeOrchardWitnessParts struct{}

func (_ FfiDestroyerTypeOrchardWitnessParts) Destroy(value OrchardWitnessParts) {
	value.Destroy()
}

type Output struct {
	Address string
	Amount  uint64
	Memo    string
}

func (r *Output) Destroy() {
	FfiDestroyerString{}.Destroy(r.Address)
	FfiDestroyerUint64{}.Destroy(r.Amount)
	FfiDestroyerString{}.Destroy(r.Memo)
}

type FfiConverterTypeOutput struct{}

var FfiConverterTypeOutputINSTANCE = FfiConverterTypeOutput{}

func (c FfiConverterTypeOutput) Lift(rb RustBufferI) Output {
	return LiftFromRustBuffer[Output](c, rb)
}

func (c FfiConverterTypeOutput) Read(reader io.Reader) Output {
	return Output{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeOutput) Lower(value Output) RustBuffer {
	return LowerIntoRustBuffer[Output](c, value)
}

func (c FfiConverterTypeOutput) Write(writer io.Writer, value Output) {
	FfiConverterStringINSTANCE.Write(writer, value.Address)
	FfiConverterUint64INSTANCE.Write(writer, value.Amount)
	FfiConverterStringINSTANCE.Write(writer, value.Memo)
}

type FfiDestroyerTypeOutput struct{}

func (_ FfiDestroyerTypeOutput) Destroy(value Output) {
	value.Destroy()
}

type PartialTx struct {
	Height       uint32
	Txid         []byte
	Inputs       []Utxo
	Outputs      []Output
	Fee          uint64
	Sighashes    [][]byte
	ExpiryHeight uint32
	Version      uint32
}

func (r *PartialTx) Destroy() {
	FfiDestroyerUint32{}.Destroy(r.Height)
	FfiDestroyerBytes{}.Destroy(r.Txid)
	FfiDestroyerSequenceTypeUtxo{}.Destroy(r.Inputs)
	FfiDestroyerSequenceTypeOutput{}.Destroy(r.Outputs)
	FfiDestroyerUint64{}.Destroy(r.Fee)
	FfiDestroyerSequenceBytes{}.Destroy(r.Sighashes)
	FfiDestroyerUint32{}.Destroy(r.ExpiryHeight)
	FfiDestroyerUint32{}.Destroy(r.Version)
}

type FfiConverterTypePartialTx struct{}

var FfiConverterTypePartialTxINSTANCE = FfiConverterTypePartialTx{}

func (c FfiConverterTypePartialTx) Lift(rb RustBufferI) PartialTx {
	return LiftFromRustBuffer[PartialTx](c, rb)
}

func (c FfiConverterTypePartialTx) Read(reader io.Reader) PartialTx {
	return PartialTx{
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterSequenceTypeUTXOINSTANCE.Read(reader),
		FfiConverterSequenceTypeOutputINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterSequenceBytesINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
	}
}

func (c FfiConverterTypePartialTx) Lower(value PartialTx) RustBuffer {
	return LowerIntoRustBuffer[PartialTx](c, value)
}

func (c FfiConverterTypePartialTx) Write(writer io.Writer, value PartialTx) {
	FfiConverterUint32INSTANCE.Write(writer, value.Height)
	FfiConverterBytesINSTANCE.Write(writer, value.Txid)
	FfiConverterSequenceTypeUTXOINSTANCE.Write(writer, value.Inputs)
	FfiConverterSequenceTypeOutputINSTANCE.Write(writer, value.Outputs)
	FfiConverterUint64INSTANCE.Write(writer, value.Fee)
	FfiConverterSequenceBytesINSTANCE.Write(writer, value.Sighashes)
	FfiConverterUint32INSTANCE.Write(writer, value.ExpiryHeight)
	FfiConverterUint32INSTANCE.Write(writer, value.Version)
}

type FfiDestroyerTypePartialTx struct{}

func (_ FfiDestroyerTypePartialTx) Destroy(value PartialTx) {
	value.Destroy()
}

type SpendableNote struct {
	Diversifier []byte
	Value       uint64
	Rseed       []byte
	Zip212      bool
	Witness     []byte
}

func (r *SpendableNote) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Diversifier)
	FfiDestroyerUint64{}.Destroy(r.Value)
	FfiDestroyerBytes{}.Destroy(r.Rseed)
	FfiDestroyerBool{}.Destroy(r.Zip212)
	FfiDestroyerBytes{}.Destroy(r.Witness)
}

type FfiConverterTypeSpendableNote struct{}

var FfiConverterTypeSpendableNoteINSTANCE = FfiConverterTypeSpendableNote{}

func (c FfiConverterTypeSpendableNote) Lift(rb RustBufferI) SpendableNote {
	return LiftFromRustBuffer[SpendableNote](c, rb)
}

func (c FfiConverterTypeSpendableNote) Read(reader io.Reader) SpendableNote {
	return SpendableNote{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeSpendableNote) Lower(value SpendableNote) RustBuffer {
	return LowerIntoRustBuffer[SpendableNote](c, value)
}

func (c FfiConverterTypeSpendableNote) Write(writer io.Writer, value SpendableNote) {
	FfiConverterBytesINSTANCE.Write(writer, value.Diversifier)
	FfiConverterUint64INSTANCE.Write(writer, value.Value)
	FfiConverterBytesINSTANCE.Write(writer, value.Rseed)
	FfiConverterBoolINSTANCE.Write(writer, value.Zip212)
	FfiConverterBytesINSTANCE.Write(writer, value.Witness)
}

type FfiDestroyerTypeSpendableNote struct{}

func (_ FfiDestroyerTypeSpendableNote) Destroy(value SpendableNote) {
	value.Destroy()
}

type Utxo struct {
	Txid   string
	Height uint32
	Vout   uint32
	Script string
	Value  uint64
}

func (r *Utxo) Destroy() {
	FfiDestroyerString{}.Destroy(r.Txid)
	FfiDestroyerUint32{}.Destroy(r.Height)
	FfiDestroyerUint32{}.Destroy(r.Vout)
	FfiDestroyerString{}.Destroy(r.Script)
	FfiDestroyerUint64{}.Destroy(r.Value)
}

type FfiConverterTypeUTXO struct{}

var FfiConverterTypeUTXOINSTANCE = FfiConverterTypeUTXO{}

func (c FfiConverterTypeUTXO) Lift(rb RustBufferI) Utxo {
	return LiftFromRustBuffer[Utxo](c, rb)
}

func (c FfiConverterTypeUTXO) Read(reader io.Reader) Utxo {
	return Utxo{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeUTXO) Lower(value Utxo) RustBuffer {
	return LowerIntoRustBuffer[Utxo](c, value)
}

func (c FfiConverterTypeUTXO) Write(writer io.Writer, value Utxo) {
	FfiConverterStringINSTANCE.Write(writer, value.Txid)
	FfiConverterUint32INSTANCE.Write(writer, value.Height)
	FfiConverterUint32INSTANCE.Write(writer, value.Vout)
	FfiConverterStringINSTANCE.Write(writer, value.Script)
	FfiConverterUint64INSTANCE.Write(writer, value.Value)
}

type FfiDestroyerTypeUtxo struct{}

func (_ FfiDestroyerTypeUtxo) Destroy(value Utxo) {
	value.Destroy()
}

type WitnessParts struct {
	WitnessBytes []byte
	AnchorBytes  []byte
}

func (r *WitnessParts) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.WitnessBytes)
	FfiDestroyerBytes{}.Destroy(r.AnchorBytes)
}

type FfiConverterTypeWitnessParts struct{}

var FfiConverterTypeWitnessPartsINSTANCE = FfiConverterTypeWitnessParts{}

func (c FfiConverterTypeWitnessParts) Lift(rb RustBufferI) WitnessParts {
	return LiftFromRustBuffer[WitnessParts](c, rb)
}

func (c FfiConverterTypeWitnessParts) Read(reader io.Reader) WitnessParts {
	return WitnessParts{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeWitnessParts) Lower(value WitnessParts) RustBuffer {
	return LowerIntoRustBuffer[WitnessParts](c, value)
}

func (c FfiConverterTypeWitnessParts) Write(writer io.Writer, value WitnessParts) {
	FfiConverterBytesINSTANCE.Write(writer, value.WitnessBytes)
	FfiConverterBytesINSTANCE.Write(writer, value.AnchorBytes)
}

type FfiDestroyerTypeWitnessParts struct{}

func (_ FfiDestroyerTypeWitnessParts) Destroy(value WitnessParts) {
	value.Destroy()
}

type ZecAccount struct {
	PrivateKey  []byte
	PublicKey   []byte
	Index       uint32
	Transparent string
	Sapling     string
	Orchard     string
	Unified     string
}

func (r *ZecAccount) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.PrivateKey)
	FfiDestroyerBytes{}.Destroy(r.PublicKey)
	FfiDestroyerUint32{}.Destroy(r.Index)
	FfiDestroyerString{}.Destroy(r.Transparent)
	FfiDestroyerString{}.Destroy(r.Sapling)
	FfiDestroyerString{}.Destroy(r.Orchard)
	FfiDestroyerString{}.Destroy(r.Unified)
}

type FfiConverterTypeZecAccount struct{}

var FfiConverterTypeZecAccountINSTANCE = FfiConverterTypeZecAccount{}

func (c FfiConverterTypeZecAccount) Lift(rb RustBufferI) ZecAccount {
	return LiftFromRustBuffer[ZecAccount](c, rb)
}

func (c FfiConverterTypeZecAccount) Read(reader io.Reader) ZecAccount {
	return ZecAccount{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeZecAccount) Lower(value ZecAccount) RustBuffer {
	return LowerIntoRustBuffer[ZecAccount](c, value)
}

func (c FfiConverterTypeZecAccount) Write(writer io.Writer, value ZecAccount) {
	FfiConverterBytesINSTANCE.Write(writer, value.PrivateKey)
	FfiConverterBytesINSTANCE.Write(writer, value.PublicKey)
	FfiConverterUint32INSTANCE.Write(writer, value.Index)
	FfiConverterStringINSTANCE.Write(writer, value.Transparent)
	FfiConverterStringINSTANCE.Write(writer, value.Sapling)
	FfiConverterStringINSTANCE.Write(writer, value.Orchard)
	FfiConverterStringINSTANCE.Write(writer, value.Unified)
}

type FfiDestroyerTypeZecAccount struct{}

func (_ FfiDestroyerTypeZecAccount) Destroy(value ZecAccount) {
	value.Destroy()
}

type BranchId uint

const (
	BranchIdOverwinter BranchId = 1
	BranchIdSapling    BranchId = 2
	BranchIdBlossom    BranchId = 3
	BranchIdHeartwood  BranchId = 4
	BranchIdCanopy     BranchId = 5
	BranchIdNu5        BranchId = 6
)

type FfiConverterTypeBranchId struct{}

var FfiConverterTypeBranchIdINSTANCE = FfiConverterTypeBranchId{}

func (c FfiConverterTypeBranchId) Lift(rb RustBufferI) BranchId {
	return LiftFromRustBuffer[BranchId](c, rb)
}

func (c FfiConverterTypeBranchId) Lower(value BranchId) RustBuffer {
	return LowerIntoRustBuffer[BranchId](c, value)
}

func (FfiConverterTypeBranchId) Read(reader io.Reader) BranchId {
	id := readInt32(reader)
	return BranchId(id)
}

func (FfiConverterTypeBranchId) Write(writer io.Writer, value BranchId) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerTypeBranchId struct{}

func (_ FfiDestroyerTypeBranchId) Destroy(value BranchId) {
}

type Network uint

const (
	NetworkMain    Network = 1
	NetworkRegtest Network = 2
	NetworkTest    Network = 3
)

type FfiConverterTypeNetwork struct{}

var FfiConverterTypeNetworkINSTANCE = FfiConverterTypeNetwork{}

func (c FfiConverterTypeNetwork) Lift(rb RustBufferI) Network {
	return LiftFromRustBuffer[Network](c, rb)
}

func (c FfiConverterTypeNetwork) Lower(value Network) RustBuffer {
	return LowerIntoRustBuffer[Network](c, value)
}

func (FfiConverterTypeNetwork) Read(reader io.Reader) Network {
	id := readInt32(reader)
	return Network(id)
}

func (FfiConverterTypeNetwork) Write(writer io.Writer, value Network) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerTypeNetwork struct{}

func (_ FfiDestroyerTypeNetwork) Destroy(value Network) {
}

type ZecError struct {
	err error
}

func (err ZecError) Error() string {
	return fmt.Sprintf("ZecError: %s", err.err.Error())
}

func (err ZecError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var (
	ErrZecErrorGenericError                = fmt.Errorf("ZecErrorGenericError")
	ErrZecErrorInvalidVaultPubkey          = fmt.Errorf("ZecErrorInvalidVaultPubkey")
	ErrZecErrorInvalidAddress              = fmt.Errorf("ZecErrorInvalidAddress")
	ErrZecErrorInitError                   = fmt.Errorf("ZecErrorInitError")
	ErrZecErrorInvalidMemo                 = fmt.Errorf("ZecErrorInvalidMemo")
	ErrZecErrorInvalidAmount               = fmt.Errorf("ZecErrorInvalidAmount")
	ErrZecErrorInvalidOvk                  = fmt.Errorf("ZecErrorInvalidOvk")
	ErrZecErrorScanError                   = fmt.Errorf("ZecErrorScanError")
	ErrZecErrorNonTransparentRefundAddress = fmt.Errorf("ZecErrorNonTransparentRefundAddress")
)

// Variant structs
type ZecErrorGenericError struct {
	message string
}

func NewZecErrorGenericError() *ZecError {
	return &ZecError{
		err: &ZecErrorGenericError{},
	}
}

func (err ZecErrorGenericError) Error() string {
	return fmt.Sprintf("GenericError: %s", err.message)
}

func (self ZecErrorGenericError) Is(target error) bool {
	return target == ErrZecErrorGenericError
}

type ZecErrorInvalidVaultPubkey struct {
	message string
}

func NewZecErrorInvalidVaultPubkey() *ZecError {
	return &ZecError{
		err: &ZecErrorInvalidVaultPubkey{},
	}
}

func (err ZecErrorInvalidVaultPubkey) Error() string {
	return fmt.Sprintf("InvalidVaultPubkey: %s", err.message)
}

func (self ZecErrorInvalidVaultPubkey) Is(target error) bool {
	return target == ErrZecErrorInvalidVaultPubkey
}

type ZecErrorInvalidAddress struct {
	message string
}

func NewZecErrorInvalidAddress() *ZecError {
	return &ZecError{
		err: &ZecErrorInvalidAddress{},
	}
}

func (err ZecErrorInvalidAddress) Error() string {
	return fmt.Sprintf("InvalidAddress: %s", err.message)
}

func (self ZecErrorInvalidAddress) Is(target error) bool {
	return target == ErrZecErrorInvalidAddress
}

type ZecErrorInitError struct {
	message string
}

func NewZecErrorInitError() *ZecError {
	return &ZecError{
		err: &ZecErrorInitError{},
	}
}

func (err ZecErrorInitError) Error() string {
	return fmt.Sprintf("InitError: %s", err.message)
}

func (self ZecErrorInitError) Is(target error) bool {
	return target == ErrZecErrorInitError
}

type ZecErrorInvalidMemo struct {
	message string
}

func NewZecErrorInvalidMemo() *ZecError {
	return &ZecError{
		err: &ZecErrorInvalidMemo{},
	}
}

func (err ZecErrorInvalidMemo) Error() string {
	return fmt.Sprintf("InvalidMemo: %s", err.message)
}

func (self ZecErrorInvalidMemo) Is(target error) bool {
	return target == ErrZecErrorInvalidMemo
}

type ZecErrorInvalidAmount struct {
	message string
}

func NewZecErrorInvalidAmount() *ZecError {
	return &ZecError{
		err: &ZecErrorInvalidAmount{},
	}
}

func (err ZecErrorInvalidAmount) Error() string {
	return fmt.Sprintf("InvalidAmount: %s", err.message)
}

func (self ZecErrorInvalidAmount) Is(target error) bool {
	return target == ErrZecErrorInvalidAmount
}

type ZecErrorInvalidOvk struct {
	message string
}

func NewZecErrorInvalidOvk() *ZecError {
	return &ZecError{
		err: &ZecErrorInvalidOvk{},
	}
}

func (err ZecErrorInvalidOvk) Error() string {
	return fmt.Sprintf("InvalidOvk: %s", err.message)
}

func (self ZecErrorInvalidOvk) Is(target error) bool {
	return target == ErrZecErrorInvalidOvk
}

type ZecErrorScanError struct {
	message string
}

func NewZecErrorScanError() *ZecError {
	return &ZecError{
		err: &ZecErrorScanError{},
	}
}

func (err ZecErrorScanError) Error() string {
	return fmt.Sprintf("ScanError: %s", err.message)
}

func (self ZecErrorScanError) Is(target error) bool {
	return target == ErrZecErrorScanError
}

type ZecErrorNonTransparentRefundAddress struct {
	message string
}

func NewZecErrorNonTransparentRefundAddress() *ZecError {
	return &ZecError{
		err: &ZecErrorNonTransparentRefundAddress{},
	}
}

func (err ZecErrorNonTransparentRefundAddress) Error() string {
	return fmt.Sprintf("NonTransparentRefundAddress: %s", err.message)
}

func (self ZecErrorNonTransparentRefundAddress) Is(target error) bool {
	return target == ErrZecErrorNonTransparentRefundAddress
}

type FfiConverterTypeZecError struct{}

var FfiConverterTypeZecErrorINSTANCE = FfiConverterTypeZecError{}

func (c FfiConverterTypeZecError) Lift(eb RustBufferI) error {
	return LiftFromRustBuffer[error](c, eb)
}

func (c FfiConverterTypeZecError) Lower(value *ZecError) RustBuffer {
	return LowerIntoRustBuffer[*ZecError](c, value)
}

func (c FfiConverterTypeZecError) Read(reader io.Reader) error {
	errorID := readUint32(reader)

	message := FfiConverterStringINSTANCE.Read(reader)
	switch errorID {
	case 1:
		return &ZecError{&ZecErrorGenericError{message}}
	case 2:
		return &ZecError{&ZecErrorInvalidVaultPubkey{message}}
	case 3:
		return &ZecError{&ZecErrorInvalidAddress{message}}
	case 4:
		return &ZecError{&ZecErrorInitError{message}}
	case 5:
		return &ZecError{&ZecErrorInvalidMemo{message}}
	case 6:
		return &ZecError{&ZecErrorInvalidAmount{message}}
	case 7:
		return &ZecError{&ZecErrorInvalidOvk{message}}
	case 8:
		return &ZecError{&ZecErrorScanError{message}}
	case 9:
		return &ZecError{&ZecErrorNonTransparentRefundAddress{message}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterTypeZecError.Read()", errorID))
	}
}

func (c FfiConverterTypeZecError) Write(writer io.Writer, value *ZecError) {
	switch variantValue := value.err.(type) {
	case *ZecErrorGenericError:
		writeInt32(writer, 1)
	case *ZecErrorInvalidVaultPubkey:
		writeInt32(writer, 2)
	case *ZecErrorInvalidAddress:
		writeInt32(writer, 3)
	case *ZecErrorInitError:
		writeInt32(writer, 4)
	case *ZecErrorInvalidMemo:
		writeInt32(writer, 5)
	case *ZecErrorInvalidAmount:
		writeInt32(writer, 6)
	case *ZecErrorInvalidOvk:
		writeInt32(writer, 7)
	case *ZecErrorScanError:
		writeInt32(writer, 8)
	case *ZecErrorNonTransparentRefundAddress:
		writeInt32(writer, 9)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterTypeZecError.Write", value))
	}
}

type FfiConverterOptionalString struct{}

var FfiConverterOptionalStringINSTANCE = FfiConverterOptionalString{}

func (c FfiConverterOptionalString) Lift(rb RustBufferI) *string {
	return LiftFromRustBuffer[*string](c, rb)
}

func (_ FfiConverterOptionalString) Read(reader io.Reader) *string {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterStringINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalString) Lower(value *string) RustBuffer {
	return LowerIntoRustBuffer[*string](c, value)
}

func (_ FfiConverterOptionalString) Write(writer io.Writer, value *string) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalString struct{}

func (_ FfiDestroyerOptionalString) Destroy(value *string) {
	if value != nil {
		FfiDestroyerString{}.Destroy(*value)
	}
}

type FfiConverterOptionalBytes struct{}

var FfiConverterOptionalBytesINSTANCE = FfiConverterOptionalBytes{}

func (c FfiConverterOptionalBytes) Lift(rb RustBufferI) *[]byte {
	return LiftFromRustBuffer[*[]byte](c, rb)
}

func (_ FfiConverterOptionalBytes) Read(reader io.Reader) *[]byte {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBytesINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBytes) Lower(value *[]byte) RustBuffer {
	return LowerIntoRustBuffer[*[]byte](c, value)
}

func (_ FfiConverterOptionalBytes) Write(writer io.Writer, value *[]byte) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBytesINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBytes struct{}

func (_ FfiDestroyerOptionalBytes) Destroy(value *[]byte) {
	if value != nil {
		FfiDestroyerBytes{}.Destroy(*value)
	}
}

type FfiConverterSequenceBytes struct{}

var FfiConverterSequenceBytesINSTANCE = FfiConverterSequenceBytes{}

func (c FfiConverterSequenceBytes) Lift(rb RustBufferI) [][]byte {
	return LiftFromRustBuffer[[][]byte](c, rb)
}

func (c FfiConverterSequenceBytes) Read(reader io.Reader) [][]byte {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([][]byte, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterBytesINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceBytes) Lower(value [][]byte) RustBuffer {
	return LowerIntoRustBuffer[[][]byte](c, value)
}

func (c FfiConverterSequenceBytes) Write(writer io.Writer, value [][]byte) {
	if len(value) > math.MaxInt32 {
		panic("[][]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterBytesINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceBytes struct{}

func (FfiDestroyerSequenceBytes) Destroy(sequence [][]byte) {
	for _, value := range sequence {
		FfiDestroyerBytes{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeDecryptedNote struct{}

var FfiConverterSequenceTypeDecryptedNoteINSTANCE = FfiConverterSequenceTypeDecryptedNote{}

func (c FfiConverterSequenceTypeDecryptedNote) Lift(rb RustBufferI) []DecryptedNote {
	return LiftFromRustBuffer[[]DecryptedNote](c, rb)
}

func (c FfiConverterSequenceTypeDecryptedNote) Read(reader io.Reader) []DecryptedNote {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]DecryptedNote, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeDecryptedNoteINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeDecryptedNote) Lower(value []DecryptedNote) RustBuffer {
	return LowerIntoRustBuffer[[]DecryptedNote](c, value)
}

func (c FfiConverterSequenceTypeDecryptedNote) Write(writer io.Writer, value []DecryptedNote) {
	if len(value) > math.MaxInt32 {
		panic("[]DecryptedNote is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeDecryptedNoteINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeDecryptedNote struct{}

func (FfiDestroyerSequenceTypeDecryptedNote) Destroy(sequence []DecryptedNote) {
	for _, value := range sequence {
		FfiDestroyerTypeDecryptedNote{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeOrchardDecryptedNote struct{}

var FfiConverterSequenceTypeOrchardDecryptedNoteINSTANCE = FfiConverterSequenceTypeOrchardDecryptedNote{}

func (c FfiConverterSequenceTypeOrchardDecryptedNote) Lift(rb RustBufferI) []OrchardDecryptedNote {
	return LiftFromRustBuffer[[]OrchardDecryptedNote](c, rb)
}

func (c FfiConverterSequenceTypeOrchardDecryptedNote) Read(reader io.Reader) []OrchardDecryptedNote {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]OrchardDecryptedNote, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeOrchardDecryptedNoteINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeOrchardDecryptedNote) Lower(value []OrchardDecryptedNote) RustBuffer {
	return LowerIntoRustBuffer[[]OrchardDecryptedNote](c, value)
}

func (c FfiConverterSequenceTypeOrchardDecryptedNote) Write(writer io.Writer, value []OrchardDecryptedNote) {
	if len(value) > math.MaxInt32 {
		panic("[]OrchardDecryptedNote is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeOrchardDecryptedNoteINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeOrchardDecryptedNote struct{}

func (FfiDestroyerSequenceTypeOrchardDecryptedNote) Destroy(sequence []OrchardDecryptedNote) {
	for _, value := range sequence {
		FfiDestroyerTypeOrchardDecryptedNote{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeOrchardSpendableNote struct{}

var FfiConverterSequenceTypeOrchardSpendableNoteINSTANCE = FfiConverterSequenceTypeOrchardSpendableNote{}

func (c FfiConverterSequenceTypeOrchardSpendableNote) Lift(rb RustBufferI) []OrchardSpendableNote {
	return LiftFromRustBuffer[[]OrchardSpendableNote](c, rb)
}

func (c FfiConverterSequenceTypeOrchardSpendableNote) Read(reader io.Reader) []OrchardSpendableNote {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]OrchardSpendableNote, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeOrchardSpendableNoteINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeOrchardSpendableNote) Lower(value []OrchardSpendableNote) RustBuffer {
	return LowerIntoRustBuffer[[]OrchardSpendableNote](c, value)
}

func (c FfiConverterSequenceTypeOrchardSpendableNote) Write(writer io.Writer, value []OrchardSpendableNote) {
	if len(value) > math.MaxInt32 {
		panic("[]OrchardSpendableNote is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeOrchardSpendableNoteINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeOrchardSpendableNote struct{}

func (FfiDestroyerSequenceTypeOrchardSpendableNote) Destroy(sequence []OrchardSpendableNote) {
	for _, value := range sequence {
		FfiDestroyerTypeOrchardSpendableNote{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeOutput struct{}

var FfiConverterSequenceTypeOutputINSTANCE = FfiConverterSequenceTypeOutput{}

func (c FfiConverterSequenceTypeOutput) Lift(rb RustBufferI) []Output {
	return LiftFromRustBuffer[[]Output](c, rb)
}

func (c FfiConverterSequenceTypeOutput) Read(reader io.Reader) []Output {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]Output, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeOutputINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeOutput) Lower(value []Output) RustBuffer {
	return LowerIntoRustBuffer[[]Output](c, value)
}

func (c FfiConverterSequenceTypeOutput) Write(writer io.Writer, value []Output) {
	if len(value) > math.MaxInt32 {
		panic("[]Output is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeOutputINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeOutput struct{}

func (FfiDestroyerSequenceTypeOutput) Destroy(sequence []Output) {
	for _, value := range sequence {
		FfiDestroyerTypeOutput{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeSpendableNote struct{}

var FfiConverterSequenceTypeSpendableNoteINSTANCE = FfiConverterSequenceTypeSpendableNote{}

func (c FfiConverterSequenceTypeSpendableNote) Lift(rb RustBufferI) []SpendableNote {
	return LiftFromRustBuffer[[]SpendableNote](c, rb)
}

func (c FfiConverterSequenceTypeSpendableNote) Read(reader io.Reader) []SpendableNote {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]SpendableNote, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeSpendableNoteINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeSpendableNote) Lower(value []SpendableNote) RustBuffer {
	return LowerIntoRustBuffer[[]SpendableNote](c, value)
}

func (c FfiConverterSequenceTypeSpendableNote) Write(writer io.Writer, value []SpendableNote) {
	if len(value) > math.MaxInt32 {
		panic("[]SpendableNote is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeSpendableNoteINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeSpendableNote struct{}

func (FfiDestroyerSequenceTypeSpendableNote) Destroy(sequence []SpendableNote) {
	for _, value := range sequence {
		FfiDestroyerTypeSpendableNote{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeUTXO struct{}

var FfiConverterSequenceTypeUTXOINSTANCE = FfiConverterSequenceTypeUTXO{}

func (c FfiConverterSequenceTypeUTXO) Lift(rb RustBufferI) []Utxo {
	return LiftFromRustBuffer[[]Utxo](c, rb)
}

func (c FfiConverterSequenceTypeUTXO) Read(reader io.Reader) []Utxo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]Utxo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeUTXOINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeUTXO) Lower(value []Utxo) RustBuffer {
	return LowerIntoRustBuffer[[]Utxo](c, value)
}

func (c FfiConverterSequenceTypeUTXO) Write(writer io.Writer, value []Utxo) {
	if len(value) > math.MaxInt32 {
		panic("[]Utxo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeUTXOINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeUtxo struct{}

func (FfiDestroyerSequenceTypeUtxo) Destroy(sequence []Utxo) {
	for _, value := range sequence {
		FfiDestroyerTypeUtxo{}.Destroy(value)
	}
}

type FfiConverterSequenceSequenceBytes struct{}

var FfiConverterSequenceSequenceBytesINSTANCE = FfiConverterSequenceSequenceBytes{}

func (c FfiConverterSequenceSequenceBytes) Lift(rb RustBufferI) [][][]byte {
	return LiftFromRustBuffer[[][][]byte](c, rb)
}

func (c FfiConverterSequenceSequenceBytes) Read(reader io.Reader) [][][]byte {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([][][]byte, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterSequenceBytesINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceSequenceBytes) Lower(value [][][]byte) RustBuffer {
	return LowerIntoRustBuffer[[][][]byte](c, value)
}

func (c FfiConverterSequenceSequenceBytes) Write(writer io.Writer, value [][][]byte) {
	if len(value) > math.MaxInt32 {
		panic("[][][]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterSequenceBytesINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceSequenceBytes struct{}

func (FfiDestroyerSequenceSequenceBytes) Destroy(sequence [][][]byte) {
	for _, value := range sequence {
		FfiDestroyerSequenceBytes{}.Destroy(value)
	}
}

func ApplySignatures(vault []byte, ptx PartialTx, signatures [][]byte, network Network) ([]byte, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_apply_signatures(FfiConverterBytesINSTANCE.Lower(vault), FfiConverterTypePartialTxINSTANCE.Lower(ptx), FfiConverterSequenceBytesINSTANCE.Lower(signatures), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func BuildOrchardWitnessFromBlocks(mnemonic string, network Network, diversifier []byte, value uint64, rseed []byte, rho []byte, cmx []byte, blocksTxs [][][]byte, branchIdAtAnchor BranchId, expectedRoot *[]byte) (OrchardWitnessParts, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_build_orchard_witness_from_blocks(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), FfiConverterBytesINSTANCE.Lower(diversifier), FfiConverterUint64INSTANCE.Lower(value), FfiConverterBytesINSTANCE.Lower(rseed), FfiConverterBytesINSTANCE.Lower(rho), FfiConverterBytesINSTANCE.Lower(cmx), FfiConverterSequenceSequenceBytesINSTANCE.Lower(blocksTxs), FfiConverterTypeBranchIdINSTANCE.Lower(branchIdAtAnchor), FfiConverterOptionalBytesINSTANCE.Lower(expectedRoot), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue OrchardWitnessParts
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeOrchardWitnessPartsINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func BuildPtx(vault []byte, ptx PartialTx, network Network) (PartialTx, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_build_ptx(FfiConverterBytesINSTANCE.Lower(vault), FfiConverterTypePartialTxINSTANCE.Lower(ptx), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PartialTx
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePartialTxINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func BuildWitnessFromBlocks(mnemonic string, network Network, diversifier []byte, value uint64, rseed []byte, zip212 bool, blocksTxs [][][]byte, branchIdAtAnchor BranchId, expectedRoot *[]byte) (WitnessParts, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_build_witness_from_blocks(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), FfiConverterBytesINSTANCE.Lower(diversifier), FfiConverterUint64INSTANCE.Lower(value), FfiConverterBytesINSTANCE.Lower(rseed), FfiConverterBoolINSTANCE.Lower(zip212), FfiConverterSequenceSequenceBytesINSTANCE.Lower(blocksTxs), FfiConverterTypeBranchIdINSTANCE.Lower(branchIdAtAnchor), FfiConverterOptionalBytesINSTANCE.Lower(expectedRoot), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue WitnessParts
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeWitnessPartsINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func ComputeTxid(vault []byte, ptx PartialTx, network Network) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_compute_txid(FfiConverterBytesINSTANCE.Lower(vault), FfiConverterTypePartialTxINSTANCE.Lower(ptx), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func DecryptNotesInTx(mnemonic string, network Network, txBytes []byte, minedHeight uint32) ([]DecryptedNote, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_decrypt_notes_in_tx(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), FfiConverterBytesINSTANCE.Lower(txBytes), FfiConverterUint32INSTANCE.Lower(minedHeight), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []DecryptedNote
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSequenceTypeDecryptedNoteINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func DecryptOrchardNotesInTx(mnemonic string, network Network, txBytes []byte, minedHeight uint32) ([]OrchardDecryptedNote, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_decrypt_orchard_notes_in_tx(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), FfiConverterBytesINSTANCE.Lower(txBytes), FfiConverterUint32INSTANCE.Lower(minedHeight), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []OrchardDecryptedNote
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSequenceTypeOrchardDecryptedNoteINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func DeriveAccountFromMnemonic(mnemonic string, network Network) (ZecAccount, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_derive_account_from_mnemonic(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue ZecAccount
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeZecAccountINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func GetOvk(vault []byte) ([]byte, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_get_ovk(FfiConverterBytesINSTANCE.Lower(vault), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func InitZec() error {
	_, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_zec_fn_func_init_zec(_uniffiStatus)
		return false
	})
	return _uniffiErr
}

func IsTransparentAddress(address string, network Network) bool {
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_zec_fn_func_is_transparent_address(FfiConverterStringINSTANCE.Lower(address), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
	}))
}

func SendFromOrchardToTransparent(mnemonic string, network Network, notesToSpend []OrchardSpendableNote, toTransparentAddress string, anchorBytes []byte, targetHeight uint32, memo *string) ([]byte, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_send_from_orchard_to_transparent(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), FfiConverterSequenceTypeOrchardSpendableNoteINSTANCE.Lower(notesToSpend), FfiConverterStringINSTANCE.Lower(toTransparentAddress), FfiConverterBytesINSTANCE.Lower(anchorBytes), FfiConverterUint32INSTANCE.Lower(targetHeight), FfiConverterOptionalStringINSTANCE.Lower(memo), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func SendFromSaplingToTransparent(mnemonic string, network Network, notesToSpend []SpendableNote, toTransparentAddress string, anchorBytes []byte, targetHeight uint32, memo *string) ([]byte, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return C.uniffi_zec_fn_func_send_from_sapling_to_transparent(FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterTypeNetworkINSTANCE.Lower(network), FfiConverterSequenceTypeSpendableNoteINSTANCE.Lower(notesToSpend), FfiConverterStringINSTANCE.Lower(toTransparentAddress), FfiConverterBytesINSTANCE.Lower(anchorBytes), FfiConverterUint32INSTANCE.Lower(targetHeight), FfiConverterOptionalStringINSTANCE.Lower(memo), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func ValidateAddressV1(address string, network Network) error {
	_, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_zec_fn_func_validate_address_v1(FfiConverterStringINSTANCE.Lower(address), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func ValidateAddressV124(address string, network Network) error {
	_, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_zec_fn_func_validate_address_v124(FfiConverterStringINSTANCE.Lower(address), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func ValidateShieldedSource(address string, network Network) (bool, error) {
	_uniffiRV, _uniffiErr := rustCallWithError(FfiConverterTypeZecError{}, func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_zec_fn_func_validate_shielded_source(FfiConverterStringINSTANCE.Lower(address), FfiConverterTypeNetworkINSTANCE.Lower(network), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue bool
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBoolINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}
