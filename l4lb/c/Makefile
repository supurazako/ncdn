# configurations
NCDN_STRIP=0
L4LB_VARIANT?=full

CFLAGS=-O2 -g -Wall -Wno-unused-value -Wno-pointer-sign
CFLAGS+=-Wno-compare-distinct-pointer-types -Wno-address-of-packed-member -Werror -fno-builtin-bcmp

ifeq (${L4LB_VARIANT},full)
else ifeq (${L4LB_VARIANT},no-stats)
CFLAGS+=-DL4LB_NO_STATS=1
else ifeq (${L4LB_VARIANT},inline-dest)
CFLAGS+=-DL4LB_INLINE_DEST=1
else ifeq (${L4LB_VARIANT},minimal)
CFLAGS+=-DL4LB_MINIMAL=1 -DL4LB_NO_STATS=1
else ifeq (${L4LB_VARIANT},pow2-dests)
CFLAGS+=-DL4LB_POW2_DESTS=1
else ifeq (${L4LB_VARIANT},keep-padding)
CFLAGS+=-DL4LB_KEEP_PADDING=1
else
$(error Unknown L4LB_VARIANT '${L4LB_VARIANT}'; expected full, no-stats, inline-dest, minimal, pow2-dests, or keep-padding)
endif

L4LB_VARIANTS=full no-stats inline-dest pow2-dests keep-padding minimal

all: lb.o dummy.o

.PHONY: variants
variants:
	@set -e; \
	for variant in ${L4LB_VARIANTS}; do \
		$(MAKE) --no-print-directory L4LB_VARIANT=$$variant lb.o; \
		cp lb.o lb-$$variant.o; \
	done
	@$(MAKE) --no-print-directory L4LB_VARIANT=full lb.o

.PHONY: all clean

.SUFFIXES:
.SUFFIXES: .o .ll .c

.c.ll:
	clang ${CFLAGS} -emit-llvm -c -o $@ $<

.ll.o:
	llc -march=bpf -filetype=obj -o $@ $<
ifeq (${NCDN_STRIP},1)
	llvm-strip -g $@
endif

lb.o: lb.c FORCE
	clang ${CFLAGS} -emit-llvm -c -o lb.ll lb.c
	llc -march=bpf -filetype=obj -o $@ lb.ll
ifeq (${NCDN_STRIP},1)
	llvm-strip -g $@
endif

%.o: %.c

.PHONY: FORCE
FORCE:

clean:
	rm -f *.ll *.o
