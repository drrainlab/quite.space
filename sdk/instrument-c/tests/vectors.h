/* A deliberately tiny reader for the flat JSON vector files: every field
 * is "name": "hexstring" or "name": number. No JSON library — the core
 * must not grow one, and the tests should not either. */
#ifndef QI_TEST_VECTORS_H
#define QI_TEST_VECTORS_H
#include <stddef.h>
#include <stdint.h>

/* Load a file into a static buffer; returns 0 on failure. */
int vec_load(const char *path);
/* Hex field → bytes; returns length or 0 if absent. */
size_t vec_hex(const char *key, uint8_t *out, size_t cap);
/* Numeric field. */
uint64_t vec_u64(const char *key);
/* String field (unescaped as-is); returns length. */
size_t vec_str(const char *key, char *out, size_t cap);

#endif
