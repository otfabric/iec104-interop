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
