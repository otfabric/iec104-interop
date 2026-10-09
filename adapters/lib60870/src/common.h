/* SPDX-License-Identifier: GPL-3.0-or-later */
/*
 * iec104-interop adapter for lib60870-C.
 *
 * This program links lib60870-C (GPL-3.0) and is distributed under the same
 * licence. It implements the container contract in docs/CONTAINER_CONTRACT.md.
 */
#ifndef IEC104_INTEROP_COMMON_H
#define IEC104_INTEROP_COMMON_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include <cjson/cJSON.h>

#include "cs104_connection.h"
#include "cs104_slave.h"

#define ADAPTER_NAME "lib60870"
#define SCHEMA_VERSION "1.0"
#define DEFAULT_FIXTURE "/fixtures/baseline/fixture.json"
#define DEFAULT_READY_FILE "/run/iec104-interop/ready"

/* Exit codes, see docs/CONTAINER_CONTRACT.md. */
#define EXIT_OK 0
#define EXIT_OPERATION_FAILED 1
#define EXIT_USAGE 2
#define EXIT_CONNECT_FAILED 3
#define EXIT_FIXTURE_INVALID 4

#ifndef ADAPTER_VERSION
#define ADAPTER_VERSION "dev"
#endif
#ifndef UPSTREAM_VERSION
#define UPSTREAM_VERSION "unknown"
#endif
#ifndef UPSTREAM_REVISION
#define UPSTREAM_REVISION "unknown"
#endif

/* ---- fixture ---- */

typedef struct {
    int ioa;
    IEC60870_5_TypeID type; /* the type without time tag */
    double value;           /* raw NVA for M_ME_NA_1 */
    bool transient;         /* M_ST_NA_1 */
    int sequence;           /* M_IT_NA_1 */
    uint8_t quality;        /* QDS bits; for M_IT_NA_1: CY 0x20, CA 0x40, IV 0x80 */
} Point;

typedef struct {
    int ioa;
    IEC60870_5_TypeID type; /* the type without time tag */
    int target;             /* index into Fixture.points */
    int reportCause;
    bool selectRequired;
    bool selected;
} Command;

typedef struct {
    int ioa;
    int name;        /* NOF */
    int size;        /* octets */
    int sectionSize; /* octets per section; the last one may be shorter */
} FileSpec;

typedef struct {
    char name[64];
    int commonAddress;
    Point* points; /* sorted by ioa */
    int pointCount;
    Command* commands;
    int commandCount;
    FileSpec* files;
    int fileCount;
} Fixture;

/* Octet i of the file at ioa: the content rule of docs/FIXTURES.md. */
static inline uint8_t fileOctet(int ioa, int i) { return (uint8_t)(((long)i + ioa) % 251); }

/* SHA-256 of data as 64 lower-case hex digits plus NUL. */
void sha256Hex(const uint8_t* data, size_t size, char out[65]);

/* Loads and validates a fixture. On failure writes a message to err. */
bool Fixture_load(const char* path, Fixture* fx, char* err, size_t errSize);
Point* Fixture_findPoint(Fixture* fx, int ioa);
Command* Fixture_findCommand(Fixture* fx, int ioa);

/* ---- type helpers ---- */

const char* typeName(int typeId); /* NULL when the adapter does not model the type */
int typeFromName(const char* name); /* 0 when unknown */
/* Maps a command type with CP56Time2a to the one without; other types map to themselves. */
IEC60870_5_TypeID baseType(IEC60870_5_TypeID type);
/* The CP56Time2a variant of a monitoring or command type, or 0. */
IEC60870_5_TypeID timeTaggedType(IEC60870_5_TypeID type);

/* A float that lib60870 encodes as exactly the given raw NVA. */
float normalizedFromRaw(int raw);
int normalizedToRaw(float value);

/* Creates the information object for a point. timestamp NULL selects the type without time tag. */
InformationObject pointToObject(const Point* p, CP56Time2a timestamp);

/* ---- JSON ---- */

cJSON* asduToJson(CS101_ASDU asdu);
void formatTime(uint64_t msSinceEpoch, char* buf, size_t size);
cJSON* qualityToJson(uint8_t quality);

/* Prints one JSON document on a single line of stdout and flushes. Takes ownership. */
void emitJson(cJSON* doc);
/* printf to stderr with an adapter prefix. */
void logf_(const char* fmt, ...) __attribute__((format(printf, 1, 2)));

/* ---- options ---- */

typedef struct {
    int argc;
    char** argv;
} Args;

/* Returns the value following --name, or def. Marks both as consumed. */
const char* argString(Args* a, const char* name, const char* def);
bool argInt(Args* a, const char* name, int def, int* out);
bool argFlag(Args* a, const char* name);
/* Returns the first argument that was not consumed, or NULL. */
const char* argUnknown(Args* a);

/* Applies --k --w --t0 --t1 --t2 --t3 to params. Returns false on a bad value. */
bool applyApciArgs(Args* a, CS104_APCIParameters params);

/* ---- commands ---- */

int runServer(Args* args);
int runClient(const char* operation, Args* args);
int printCapabilities(void);

#endif
