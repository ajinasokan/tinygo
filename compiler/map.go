package compiler

// This file emits the correct map intrinsics for map operations.

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
	"tinygo.org/x/go-llvm"
)

// emitMakeMap creates a new map object (runtime.hashmap) by allocating and
// initializing an appropriately sized object.
func (c *Compiler) emitMakeMap(frame *Frame, expr *ssa.MakeMap) (llvm.Value, error) {
	mapType := expr.Type().Underlying().(*types.Map)
	llvmKeyType := c.getLLVMType(mapType.Key().Underlying())
	llvmValueType := c.getLLVMType(mapType.Elem().Underlying())
	keySize := c.targetData.TypeAllocSize(llvmKeyType)
	valueSize := c.targetData.TypeAllocSize(llvmValueType)
	llvmKeySize := llvm.ConstInt(c.ctx.Int8Type(), keySize, false)
	llvmValueSize := llvm.ConstInt(c.ctx.Int8Type(), valueSize, false)
	sizeHint := llvm.ConstInt(c.uintptrType, 8, false)
	if expr.Reserve != nil {
		sizeHint = c.getValue(frame, expr.Reserve)
		var err error
		sizeHint, err = c.parseConvert(expr.Reserve.Type(), types.Typ[types.Uintptr], sizeHint, expr.Pos())
		if err != nil {
			return llvm.Value{}, err
		}
	}
	hashmap := c.createRuntimeCall("hashmapMake", []llvm.Value{llvmKeySize, llvmValueSize, sizeHint}, "")
	return hashmap, nil
}

func (c *Compiler) emitMapLookup(keyType, valueType types.Type, m, key llvm.Value, commaOk bool, pos token.Pos) (llvm.Value, error) {
	llvmValueType := c.getLLVMType(valueType)

	// Allocate the memory for the resulting type. Do not zero this memory: it
	// will be zeroed by the hashmap get implementation if the key is not
	// present in the map.
	mapValueAlloca, mapValuePtr, mapValueSize := c.createTemporaryAlloca(llvmValueType, "hashmap.value")

	// Do the lookup. How it is done depends on the key type.
	var commaOkValue llvm.Value
	if t, ok := keyType.(*types.Basic); ok && t.Info()&types.IsString != 0 {
		// key is a string
		params := []llvm.Value{m, key, mapValuePtr}
		commaOkValue = c.createRuntimeCall("hashmapStringGet", params, "")
	} else if hashmapIsBinaryKey(keyType) {
		// key can be compared with runtime.memequal
		// Store the key in an alloca, in the entry block to avoid dynamic stack
		// growth.
		mapKeyAlloca, mapKeyPtr, mapKeySize := c.createTemporaryAlloca(key.Type(), "hashmap.key")
		c.builder.CreateStore(key, mapKeyAlloca)
		// Fetch the value from the hashmap.
		params := []llvm.Value{m, mapKeyPtr, mapValuePtr}
		commaOkValue = c.createRuntimeCall("hashmapBinaryGet", params, "")
		c.emitLifetimeEnd(mapKeyPtr, mapKeySize)
	} else {
		// Not trivially comparable using memcmp.
		return llvm.Value{}, c.makeError(pos, "only strings, bools, ints, pointers or structs of bools/ints are supported as map keys, but got: "+keyType.String())
	}

	// Load the resulting value from the hashmap. The value is set to the zero
	// value if the key doesn't exist in the hashmap.
	mapValue := c.builder.CreateLoad(mapValueAlloca, "")
	c.emitLifetimeEnd(mapValuePtr, mapValueSize)

	if commaOk {
		tuple := llvm.Undef(c.ctx.StructType([]llvm.Type{llvmValueType, c.ctx.Int1Type()}, false))
		tuple = c.builder.CreateInsertValue(tuple, mapValue, 0, "")
		tuple = c.builder.CreateInsertValue(tuple, commaOkValue, 1, "")
		return tuple, nil
	} else {
		return mapValue, nil
	}
}

func (c *Compiler) emitMapUpdate(keyType types.Type, m, key, value llvm.Value, pos token.Pos) {
	valueAlloca, valuePtr, valueSize := c.createTemporaryAlloca(value.Type(), "hashmap.value")
	c.builder.CreateStore(value, valueAlloca)
	keyType = keyType.Underlying()
	if t, ok := keyType.(*types.Basic); ok && t.Info()&types.IsString != 0 {
		// key is a string
		params := []llvm.Value{m, key, valuePtr}
		c.createRuntimeCall("hashmapStringSet", params, "")
	} else if hashmapIsBinaryKey(keyType) {
		// key can be compared with runtime.memequal
		keyAlloca, keyPtr, keySize := c.createTemporaryAlloca(key.Type(), "hashmap.key")
		c.builder.CreateStore(key, keyAlloca)
		params := []llvm.Value{m, keyPtr, valuePtr}
		c.createRuntimeCall("hashmapBinarySet", params, "")
		c.emitLifetimeEnd(keyPtr, keySize)
	} else {
		c.addError(pos, "only strings, bools, ints, pointers or structs of bools/ints are supported as map keys, but got: "+keyType.String())
	}
	c.emitLifetimeEnd(valuePtr, valueSize)
}

func (c *Compiler) emitMapDelete(keyType types.Type, m, key llvm.Value, pos token.Pos) error {
	keyType = keyType.Underlying()
	if t, ok := keyType.(*types.Basic); ok && t.Info()&types.IsString != 0 {
		// key is a string
		params := []llvm.Value{m, key}
		c.createRuntimeCall("hashmapStringDelete", params, "")
		return nil
	} else if hashmapIsBinaryKey(keyType) {
		keyAlloca, keyPtr, keySize := c.createTemporaryAlloca(key.Type(), "hashmap.key")
		c.builder.CreateStore(key, keyAlloca)
		params := []llvm.Value{m, keyPtr}
		c.createRuntimeCall("hashmapBinaryDelete", params, "")
		c.emitLifetimeEnd(keyPtr, keySize)
		return nil
	} else {
		return c.makeError(pos, "only strings, bools, ints, pointers or structs of bools/ints are supported as map keys, but got: "+keyType.String())
	}
}

// Get FNV-1a hash of this string.
//
// https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function#FNV-1a_hash
func hashmapHash(data []byte) uint32 {
	var result uint32 = 2166136261 // FNV offset basis
	for _, c := range data {
		result ^= uint32(c)
		result *= 16777619 // FNV prime
	}
	return result
}

// Get the topmost 8 bits of the hash, without using a special value (like 0).
func hashmapTopHash(hash uint32) uint8 {
	tophash := uint8(hash >> 24)
	if tophash < 1 {
		// 0 means empty slot, so make it bigger.
		tophash += 1
	}
	return tophash
}

// Returns true if this key type does not contain strings, interfaces etc., so
// can be compared with runtime.memequal.
func hashmapIsBinaryKey(keyType types.Type) bool {
	switch keyType := keyType.(type) {
	case *types.Basic:
		return keyType.Info()&(types.IsBoolean|types.IsInteger) != 0
	case *types.Pointer:
		return true
	case *types.Struct:
		for i := 0; i < keyType.NumFields(); i++ {
			fieldType := keyType.Field(i).Type().Underlying()
			if !hashmapIsBinaryKey(fieldType) {
				return false
			}
		}
		return true
	case *types.Array:
		return hashmapIsBinaryKey(keyType.Elem())
	case *types.Named:
		return hashmapIsBinaryKey(keyType.Underlying())
	default:
		return false
	}
}
