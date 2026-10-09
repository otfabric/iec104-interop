/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <math.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static pthread_mutex_t outputLock = PTHREAD_MUTEX_INITIALIZER;

void emitJson(cJSON* doc)
{
    char* text = cJSON_PrintUnformatted(doc);
    pthread_mutex_lock(&outputLock);
    fputs(text, stdout);
    fputc('\n', stdout);
    fflush(stdout);
    pthread_mutex_unlock(&outputLock);
    cJSON_free(text);
    cJSON_Delete(doc);
}

void logf_(const char* fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    pthread_mutex_lock(&outputLock);
    fputs("[" ADAPTER_NAME "] ", stderr);
    vfprintf(stderr, fmt, ap);
    fputc('\n', stderr);
    pthread_mutex_unlock(&outputLock);
    va_end(ap);
}

static const struct {
    int id;
    const char* name;
} TYPES[] = {
    { M_SP_NA_1, "M_SP_NA_1" }, { M_DP_NA_1, "M_DP_NA_1" }, { M_ST_NA_1, "M_ST_NA_1" }, { M_BO_NA_1, "M_BO_NA_1" },
    { M_ME_NA_1, "M_ME_NA_1" }, { M_ME_NB_1, "M_ME_NB_1" }, { M_ME_NC_1, "M_ME_NC_1" }, { M_IT_NA_1, "M_IT_NA_1" },
    { M_SP_TB_1, "M_SP_TB_1" }, { M_DP_TB_1, "M_DP_TB_1" }, { M_ST_TB_1, "M_ST_TB_1" }, { M_BO_TB_1, "M_BO_TB_1" },
    { M_ME_TD_1, "M_ME_TD_1" }, { M_ME_TE_1, "M_ME_TE_1" }, { M_ME_TF_1, "M_ME_TF_1" }, { M_IT_TB_1, "M_IT_TB_1" },
    { C_SC_NA_1, "C_SC_NA_1" }, { C_DC_NA_1, "C_DC_NA_1" }, { C_RC_NA_1, "C_RC_NA_1" }, { C_SE_NA_1, "C_SE_NA_1" },
    { C_SE_NB_1, "C_SE_NB_1" }, { C_SE_NC_1, "C_SE_NC_1" }, { C_SC_TA_1, "C_SC_TA_1" }, { C_DC_TA_1, "C_DC_TA_1" },
    { C_RC_TA_1, "C_RC_TA_1" }, { C_SE_TA_1, "C_SE_TA_1" }, { C_SE_TB_1, "C_SE_TB_1" }, { C_SE_TC_1, "C_SE_TC_1" },
    { M_EI_NA_1, "M_EI_NA_1" }, { C_IC_NA_1, "C_IC_NA_1" }, { C_CI_NA_1, "C_CI_NA_1" }, { C_RD_NA_1, "C_RD_NA_1" },
    { C_CS_NA_1, "C_CS_NA_1" }, { C_TS_TA_1, "C_TS_TA_1" },
    { F_FR_NA_1, "F_FR_NA_1" }, { F_SR_NA_1, "F_SR_NA_1" }, { F_SC_NA_1, "F_SC_NA_1" }, { F_LS_NA_1, "F_LS_NA_1" },
    { F_AF_NA_1, "F_AF_NA_1" }, { F_SG_NA_1, "F_SG_NA_1" },
};

const char* typeName(int typeId)
{
    for (size_t i = 0; i < sizeof(TYPES) / sizeof(TYPES[0]); i++)
        if (TYPES[i].id == typeId)
            return TYPES[i].name;
    return NULL;
}

int typeFromName(const char* name)
{
    for (size_t i = 0; i < sizeof(TYPES) / sizeof(TYPES[0]); i++)
        if (strcmp(TYPES[i].name, name) == 0)
            return TYPES[i].id;
    return 0;
}

IEC60870_5_TypeID baseType(IEC60870_5_TypeID type)
{
    switch (type) {
    case C_SC_TA_1: return C_SC_NA_1;
    case C_DC_TA_1: return C_DC_NA_1;
    case C_RC_TA_1: return C_RC_NA_1;
    case C_SE_TA_1: return C_SE_NA_1;
    case C_SE_TB_1: return C_SE_NB_1;
    case C_SE_TC_1: return C_SE_NC_1;
    default: return type;
    }
}

IEC60870_5_TypeID timeTaggedType(IEC60870_5_TypeID type)
{
    switch (type) {
    case M_SP_NA_1: return M_SP_TB_1;
    case M_DP_NA_1: return M_DP_TB_1;
    case M_ST_NA_1: return M_ST_TB_1;
    case M_BO_NA_1: return M_BO_TB_1;
    case M_ME_NA_1: return M_ME_TD_1;
    case M_ME_NB_1: return M_ME_TE_1;
    case M_ME_NC_1: return M_ME_TF_1;
    case M_IT_NA_1: return M_IT_TB_1;
    case C_SC_NA_1: return C_SC_TA_1;
    case C_DC_NA_1: return C_DC_TA_1;
    case C_RC_NA_1: return C_RC_TA_1;
    case C_SE_NA_1: return C_SE_TA_1;
    case C_SE_NB_1: return C_SE_TB_1;
    case C_SE_NC_1: return C_SE_TC_1;
    default: return 0;
    }
}

int normalizedToRaw(float value)
{
    return NormalizedValue_toScaled(value);
}

/* lib60870 takes normalized values as float and converts with its own
 * rounding. Search the neighbourhood for a float that maps back exactly. */
float normalizedFromRaw(int raw)
{
    float f = NormalizedValue_fromScaled(raw);
    for (int i = 0; i < 64 && NormalizedValue_toScaled(f) != raw; i++)
        f = nextafterf(f, NormalizedValue_toScaled(f) < raw ? 2.0f : -2.0f);
    return f;
}

InformationObject pointToObject(const Point* p, CP56Time2a ts)
{
    int ioa = p->ioa;
    QualityDescriptor q = p->quality;
    switch (p->type) {
    case M_SP_NA_1:
        return ts ? (InformationObject)SinglePointWithCP56Time2a_create(NULL, ioa, p->value != 0, q, ts)
                  : (InformationObject)SinglePointInformation_create(NULL, ioa, p->value != 0, q);
    case M_DP_NA_1:
        return ts ? (InformationObject)DoublePointWithCP56Time2a_create(NULL, ioa, (DoublePointValue)p->value, q, ts)
                  : (InformationObject)DoublePointInformation_create(NULL, ioa, (DoublePointValue)p->value, q);
    case M_ST_NA_1:
        return ts ? (InformationObject)StepPositionWithCP56Time2a_create(NULL, ioa, (int)p->value, p->transient, q, ts)
                  : (InformationObject)StepPositionInformation_create(NULL, ioa, (int)p->value, p->transient, q);
    case M_BO_NA_1:
        return ts ? (InformationObject)Bitstring32WithCP56Time2a_create(NULL, ioa, (uint32_t)p->value, ts)
                  : (InformationObject)BitString32_create(NULL, ioa, (uint32_t)p->value);
    case M_ME_NA_1: {
        float f = normalizedFromRaw((int)p->value);
        return ts ? (InformationObject)MeasuredValueNormalizedWithCP56Time2a_create(NULL, ioa, f, q, ts)
                  : (InformationObject)MeasuredValueNormalized_create(NULL, ioa, f, q);
    }
    case M_ME_NB_1:
        return ts ? (InformationObject)MeasuredValueScaledWithCP56Time2a_create(NULL, ioa, (int)p->value, q, ts)
                  : (InformationObject)MeasuredValueScaled_create(NULL, ioa, (int)p->value, q);
    case M_ME_NC_1:
        return ts ? (InformationObject)MeasuredValueShortWithCP56Time2a_create(NULL, ioa, (float)p->value, q, ts)
                  : (InformationObject)MeasuredValueShort_create(NULL, ioa, (float)p->value, q);
    case M_IT_NA_1: {
        struct sBinaryCounterReading bcr;
        BinaryCounterReading_create(&bcr, (int32_t)p->value, p->sequence, (p->quality & 0x20) != 0,
            (p->quality & 0x40) != 0, (p->quality & 0x80) != 0);
        return ts ? (InformationObject)IntegratedTotalsWithCP56Time2a_create(NULL, ioa, &bcr, ts)
                  : (InformationObject)IntegratedTotals_create(NULL, ioa, &bcr);
    }
    default:
        return NULL;
    }
}

void formatTime(uint64_t ms, char* buf, size_t size)
{
    time_t seconds = (time_t)(ms / 1000);
    struct tm tm;
    gmtime_r(&seconds, &tm);
    size_t n = strftime(buf, size, "%Y-%m-%dT%H:%M:%S", &tm);
    snprintf(buf + n, size - n, ".%03dZ", (int)(ms % 1000));
}

cJSON* qualityToJson(uint8_t quality)
{
    cJSON* a = cJSON_CreateArray();
    if (quality & IEC60870_QUALITY_OVERFLOW) cJSON_AddItemToArray(a, cJSON_CreateString("OV"));
    if (quality & IEC60870_QUALITY_BLOCKED) cJSON_AddItemToArray(a, cJSON_CreateString("BL"));
    if (quality & IEC60870_QUALITY_SUBSTITUTED) cJSON_AddItemToArray(a, cJSON_CreateString("SB"));
    if (quality & IEC60870_QUALITY_NON_TOPICAL) cJSON_AddItemToArray(a, cJSON_CreateString("NT"));
    if (quality & IEC60870_QUALITY_INVALID) cJSON_AddItemToArray(a, cJSON_CreateString("IV"));
    return a;
}

/* ---- options ---- */

static int findArg(Args* a, const char* name)
{
    for (int i = 0; i < a->argc; i++)
        if (a->argv[i] != NULL && strcmp(a->argv[i], name) == 0)
            return i;
    return -1;
}

const char* argString(Args* a, const char* name, const char* def)
{
    int i = findArg(a, name);
    if (i < 0 || i + 1 >= a->argc || a->argv[i + 1] == NULL)
        return def;
    const char* v = a->argv[i + 1];
    a->argv[i] = NULL;
    a->argv[i + 1] = NULL;
    return v;
}

bool argInt(Args* a, const char* name, int def, int* out)
{
    const char* s = argString(a, name, NULL);
    *out = def;
    if (s == NULL)
        return true;
    char* end;
    long v = strtol(s, &end, 10);
    if (*s == 0 || *end != 0) {
        logf_("%s: '%s' is not an integer", name, s);
        return false;
    }
    *out = (int)v;
    return true;
}

bool argFlag(Args* a, const char* name)
{
    int i = findArg(a, name);
    if (i < 0)
        return false;
    a->argv[i] = NULL;
    return true;
}

const char* argUnknown(Args* a)
{
    for (int i = 0; i < a->argc; i++)
        if (a->argv[i] != NULL)
            return a->argv[i];
    return NULL;
}

bool applyApciArgs(Args* a, CS104_APCIParameters p)
{
    int k, w, t0, t1, t2, t3;
    if (!argInt(a, "--k", p->k, &k) || !argInt(a, "--w", p->w, &w) || !argInt(a, "--t0", p->t0, &t0) ||
        !argInt(a, "--t1", p->t1, &t1) || !argInt(a, "--t2", p->t2, &t2) || !argInt(a, "--t3", p->t3, &t3))
        return false;
    if (k < 1 || k > 32767 || w < 1 || w > k || t0 < 1 || t1 < 1 || t2 < 1 || t2 >= t1 || t3 < 0) {
        logf_("invalid APCI parameters: need 1 <= w <= k <= 32767, t0 > 0, 0 < t2 < t1, t3 >= 0 (seconds)");
        return false;
    }
    p->k = k;
    p->w = w;
    p->t0 = t0;
    p->t1 = t1;
    p->t2 = t2;
    p->t3 = t3;
    return true;
}

/* ---- SHA-256 (FIPS 180-4), for the file operation's result ---- */

static const uint32_t SHA_K[64] = {
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98,
    0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786,
    0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8,
    0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819,
    0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a,
    0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7,
    0xc67178f2
};

#define ROR(x, n) (((x) >> (n)) | ((x) << (32 - (n))))

static void shaBlock(uint32_t h[8], const uint8_t* p)
{
    uint32_t w[64];
    for (int i = 0; i < 16; i++)
        w[i] = (uint32_t)p[4 * i] << 24 | (uint32_t)p[4 * i + 1] << 16 | (uint32_t)p[4 * i + 2] << 8 | p[4 * i + 3];
    for (int i = 16; i < 64; i++) {
        uint32_t s0 = ROR(w[i - 15], 7) ^ ROR(w[i - 15], 18) ^ (w[i - 15] >> 3);
        uint32_t s1 = ROR(w[i - 2], 17) ^ ROR(w[i - 2], 19) ^ (w[i - 2] >> 10);
        w[i] = w[i - 16] + s0 + w[i - 7] + s1;
    }
    uint32_t a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], k = h[7];
    for (int i = 0; i < 64; i++) {
        uint32_t t1 = k + (ROR(e, 6) ^ ROR(e, 11) ^ ROR(e, 25)) + ((e & f) ^ (~e & g)) + SHA_K[i] + w[i];
        uint32_t t2 = (ROR(a, 2) ^ ROR(a, 13) ^ ROR(a, 22)) + ((a & b) ^ (a & c) ^ (b & c));
        k = g; g = f; f = e; e = d + t1; d = c; c = b; b = a; a = t1 + t2;
    }
    h[0] += a; h[1] += b; h[2] += c; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += k;
}

void sha256Hex(const uint8_t* data, size_t size, char out[65])
{
    uint32_t h[8] = { 0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19 };
    size_t full = size / 64;
    for (size_t i = 0; i < full; i++)
        shaBlock(h, data + 64 * i);
    uint8_t tail[128];
    size_t rest = size - 64 * full;
    memset(tail, 0, sizeof(tail));
    if (rest > 0)
        memcpy(tail, data + 64 * full, rest);
    tail[rest] = 0x80;
    size_t padded = rest < 56 ? 64 : 128;
    uint64_t bits = (uint64_t)size * 8;
    for (int i = 0; i < 8; i++)
        tail[padded - 1 - i] = (uint8_t)(bits >> (8 * i));
    shaBlock(h, tail);
    if (padded == 128)
        shaBlock(h, tail + 64);
    for (int i = 0; i < 8; i++)
        snprintf(out + 8 * i, 9, "%08x", h[i]);
}
