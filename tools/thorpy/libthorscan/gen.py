from cffi import FFI

ffibuilder = FFI()

ffibuilder.set_source(
    module_name="libthorscan",
    source='#include "libthorscan.h"',
    extra_objects=["libthorscan.so"],
)

ffibuilder.cdef(
    csource="""
    extern void Free(void* p);
    extern void ScanBlocks(int start);
    extern char* GetScanBlock();
    extern void ScanBlockResults(int start);
    extern char* GetScanBlockResult();
    extern void SubscribeBlocks();
    extern char* GetSubscribeBlock();
    extern void SubscribeTxs();
    extern char* GetSubscribeTx();
    extern char* TxToJSON(char* txb);
    extern char* TxFromJSON(char* txj);
    """
)

ffibuilder.compile(verbose=True)
