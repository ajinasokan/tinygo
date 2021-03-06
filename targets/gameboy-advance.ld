OUTPUT_ARCH(arm)
ENTRY(_start)

MEMORY {
    ewram   : ORIGIN = 0x02000000, LENGTH = 256K /* on-board work RAM (2 wait states) */
    iwram   : ORIGIN = 0x03000000, LENGTH = 32K  /* in-chip work RAM (faster) */
    rom     : ORIGIN = 0x08000000, LENGTH = 32M  /* flash ROM */
}

__iwram_top = ORIGIN(iwram) + LENGTH(iwram);;
_stack_size = 3K;
__sp_irq    = _stack_top;
__sp_usr    = _stack_top - 1K;

SECTIONS
{
    .text :
    {
        KEEP (*(.init))
        *(.text)
        . = ALIGN(4);
    } >rom

    .rodata :
    {
        . = ALIGN(4);
        *(.rodata)
        *(.rodata*)
        . = ALIGN(4);
    } >rom

    /* Put the stack at the bottom of RAM, so that the application will
     * crash on stack overflow instead of silently corrupting memory.
     * See: http://blog.japaric.io/stack-overflow-protection/ */
    .stack (NOLOAD) :
    {
        . = ALIGN(4);
        . += _stack_size;
        _stack_top = .;
    } >iwram

    /* Start address (in flash) of .data, used by startup code. */
    _sidata = LOADADDR(.data);

    /* Globals with initial value */
    .data :
    {
        . = ALIGN(4);
        _sdata = .;        /* used by startup code */
        *(.data)
        *(.data*)
        *(.iwram .iwram.*)
        . = ALIGN(4);
        _edata = .;        /* used by startup code */
    } >iwram AT>rom

    /* Zero-initialized globals  */
    .bss :
    {
        . = ALIGN(4);
        _sbss = .;         /* used by startup code */
        *(.bss)
        *(.bss*)
        *(COMMON)
        . = ALIGN(4);
        _ebss = .;         /* used by startup code */
    } >iwram

    /DISCARD/ :
    {
        *(.ARM.exidx)      /* causes 'no memory region specified' error in lld */
        *(.ARM.exidx.*)    /* causes spurious 'undefined reference' errors */
    }
}

/* For the memory allocator. */
_heap_start = ORIGIN(ewram);
_heap_end = ORIGIN(ewram) + LENGTH(ewram);
_globals_start = _sdata;
_globals_end = _ebss;
