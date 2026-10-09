/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static char* readFile(const char* path)
{
    FILE* f = fopen(path, "rb");
    if (f == NULL)
        return NULL;
    fseek(f, 0, SEEK_END);
    long size = ftell(f);
    fseek(f, 0, SEEK_SET);
    char* buf = malloc((size_t)size + 1);
    if (buf != NULL && fread(buf, 1, (size_t)size, f) != (size_t)size) {
        free(buf);
        buf = NULL;
    }
    if (buf != NULL)
        buf[size] = 0;
    fclose(f);
    return buf;
}

static bool fail(char* err, size_t errSize, const char* fmt, int a, const char* s)
{
    snprintf(err, errSize, fmt, a, s);
    return false;
}

static bool isInteger(const cJSON* n)
{
    return cJSON_IsNumber(n) && floor(n->valuedouble) == n->valuedouble;
}

/* Parses the quality array of a point against the flags its type may carry. */
static bool parseQuality(const cJSON* q, IEC60870_5_TypeID type, uint8_t* out)
{
    *out = 0;
    if (q == NULL)
        return true;
    if (!cJSON_IsArray(q))
        return false;
    const cJSON* item;
    cJSON_ArrayForEach(item, q)
    {
        if (!cJSON_IsString(item))
            return false;
        const char* s = item->valuestring;
        uint8_t bit;
        if (type == M_IT_NA_1) {
            if (strcmp(s, "CY") == 0) bit = 0x20;
            else if (strcmp(s, "CA") == 0) bit = 0x40;
            else if (strcmp(s, "IV") == 0) bit = 0x80;
            else return false;
        } else {
            if (strcmp(s, "OV") == 0) bit = IEC60870_QUALITY_OVERFLOW;
            else if (strcmp(s, "BL") == 0) bit = IEC60870_QUALITY_BLOCKED;
            else if (strcmp(s, "SB") == 0) bit = IEC60870_QUALITY_SUBSTITUTED;
            else if (strcmp(s, "NT") == 0) bit = IEC60870_QUALITY_NON_TOPICAL;
            else if (strcmp(s, "IV") == 0) bit = IEC60870_QUALITY_INVALID;
            else return false;
            if (bit == IEC60870_QUALITY_OVERFLOW && (type == M_SP_NA_1 || type == M_DP_NA_1))
                return false;
            if (type == M_BO_NA_1)
                return false; /* lib60870 cannot set a quality on a bitstring */
        }
        *out |= bit;
    }
    return true;
}

static int comparePoints(const void* a, const void* b)
{
    return ((const Point*)a)->ioa - ((const Point*)b)->ioa;
}

Point* Fixture_findPoint(Fixture* fx, int ioa)
{
    for (int i = 0; i < fx->pointCount; i++)
        if (fx->points[i].ioa == ioa)
            return &fx->points[i];
    return NULL;
}

Command* Fixture_findCommand(Fixture* fx, int ioa)
{
    for (int i = 0; i < fx->commandCount; i++)
        if (fx->commands[i].ioa == ioa)
            return &fx->commands[i];
    return NULL;
}

static IEC60870_5_TypeID targetTypeOf(IEC60870_5_TypeID command)
{
    switch (command) {
    case C_SC_NA_1: return M_SP_NA_1;
    case C_DC_NA_1: return M_DP_NA_1;
    case C_RC_NA_1: return M_ST_NA_1;
    case C_SE_NA_1: return M_ME_NA_1;
    case C_SE_NB_1: return M_ME_NB_1;
    case C_SE_NC_1: return M_ME_NC_1;
    default: return 0;
    }
}

bool Fixture_load(const char* path, Fixture* fx, char* err, size_t errSize)
{
    memset(fx, 0, sizeof(*fx));
    char* text = readFile(path);
    if (text == NULL)
        return fail(err, errSize, "cannot read fixture%.0d %s", 0, path);
    cJSON* root = cJSON_Parse(text);
    free(text);
    if (root == NULL)
        return fail(err, errSize, "fixture is not valid JSON%.0d%s", 0, "");

    bool ok = false;
    const cJSON* version = cJSON_GetObjectItemCaseSensitive(root, "schemaVersion");
    const cJSON* name = cJSON_GetObjectItemCaseSensitive(root, "name");
    const cJSON* station = cJSON_GetObjectItemCaseSensitive(root, "station");
    const cJSON* ca = station ? cJSON_GetObjectItemCaseSensitive(station, "commonAddress") : NULL;
    const cJSON* points = cJSON_GetObjectItemCaseSensitive(root, "points");
    const cJSON* commands = cJSON_GetObjectItemCaseSensitive(root, "commands");

    if (!cJSON_IsString(version) || strcmp(version->valuestring, SCHEMA_VERSION) != 0) {
        fail(err, errSize, "unsupported schemaVersion, want " SCHEMA_VERSION "%.0d%s", 0, "");
        goto done;
    }
    if (!cJSON_IsString(name) || !isInteger(ca) || ca->valueint < 1 || ca->valueint > 65534 ||
        !cJSON_IsArray(points) || cJSON_GetArraySize(points) < 1) {
        fail(err, errSize, "fixture needs name, station.commonAddress (1..65534) and points%.0d%s", 0, "");
        goto done;
    }
    snprintf(fx->name, sizeof(fx->name), "%s", name->valuestring);
    fx->commonAddress = ca->valueint;

    fx->points = calloc((size_t)cJSON_GetArraySize(points), sizeof(Point));
    const cJSON* item;
    cJSON_ArrayForEach(item, points)
    {
        Point* p = &fx->points[fx->pointCount];
        const cJSON* ioa = cJSON_GetObjectItemCaseSensitive(item, "ioa");
        const cJSON* type = cJSON_GetObjectItemCaseSensitive(item, "type");
        const cJSON* value = cJSON_GetObjectItemCaseSensitive(item, "value");
        const cJSON* transient = cJSON_GetObjectItemCaseSensitive(item, "transient");
        const cJSON* sequence = cJSON_GetObjectItemCaseSensitive(item, "sequence");
        if (!isInteger(ioa) || ioa->valueint < 1 || ioa->valueint > 0xFFFFFF || !cJSON_IsString(type)) {
            fail(err, errSize, "point %d: needs ioa (1..16777215) and type%s", fx->pointCount, "");
            goto done;
        }
        p->ioa = ioa->valueint;
        p->type = (IEC60870_5_TypeID)typeFromName(type->valuestring);
        if (Fixture_findPoint(fx, p->ioa) != NULL) {
            fail(err, errSize, "point %d: duplicate information object address%s", p->ioa, "");
            goto done;
        }
        double v = cJSON_IsNumber(value) ? value->valuedouble : 0;
        bool valid;
        switch (p->type) {
        case M_SP_NA_1:
            valid = cJSON_IsBool(value);
            v = cJSON_IsTrue(value) ? 1 : 0;
            break;
        case M_DP_NA_1: valid = isInteger(value) && v >= 0 && v <= 3; break;
        case M_ST_NA_1: valid = isInteger(value) && v >= -64 && v <= 63; break;
        case M_BO_NA_1: valid = isInteger(value) && v >= 0 && v <= 4294967295.0; break;
        case M_ME_NA_1:
        case M_ME_NB_1: valid = isInteger(value) && v >= -32768 && v <= 32767; break;
        case M_ME_NC_1: valid = cJSON_IsNumber(value); break;
        case M_IT_NA_1: valid = isInteger(value) && v >= -2147483648.0 && v <= 2147483647.0; break;
        default:
            fail(err, errSize, "point %d: unsupported type %s", p->ioa, type->valuestring);
            goto done;
        }
        if (!valid) {
            fail(err, errSize, "point %d: value is not valid for %s", p->ioa, type->valuestring);
            goto done;
        }
        p->value = v;
        if (transient != NULL) {
            if (p->type != M_ST_NA_1 || !cJSON_IsBool(transient)) {
                fail(err, errSize, "point %d: transient is a boolean of M_ST_NA_1 only%s", p->ioa, "");
                goto done;
            }
            p->transient = cJSON_IsTrue(transient);
        }
        if (sequence != NULL) {
            if (p->type != M_IT_NA_1 || !isInteger(sequence) || sequence->valueint < 0 || sequence->valueint > 31) {
                fail(err, errSize, "point %d: sequence is 0..31 of M_IT_NA_1 only%s", p->ioa, "");
                goto done;
            }
            p->sequence = sequence->valueint;
        }
        if (!parseQuality(cJSON_GetObjectItemCaseSensitive(item, "quality"), p->type, &p->quality)) {
            fail(err, errSize, "point %d: quality is not valid for %s", p->ioa, type->valuestring);
            goto done;
        }
        fx->pointCount++;
    }
    qsort(fx->points, (size_t)fx->pointCount, sizeof(Point), comparePoints);

    if (commands != NULL) {
        if (!cJSON_IsArray(commands)) {
            fail(err, errSize, "commands must be an array%.0d%s", 0, "");
            goto done;
        }
        fx->commands = calloc((size_t)cJSON_GetArraySize(commands) + 1, sizeof(Command));
        cJSON_ArrayForEach(item, commands)
        {
            Command* c = &fx->commands[fx->commandCount];
            const cJSON* ioa = cJSON_GetObjectItemCaseSensitive(item, "ioa");
            const cJSON* type = cJSON_GetObjectItemCaseSensitive(item, "type");
            const cJSON* target = cJSON_GetObjectItemCaseSensitive(item, "target");
            const cJSON* cause = cJSON_GetObjectItemCaseSensitive(item, "reportCause");
            const cJSON* sel = cJSON_GetObjectItemCaseSensitive(item, "selectRequired");
            if (!isInteger(ioa) || ioa->valueint < 1 || ioa->valueint > 0xFFFFFF || !cJSON_IsString(type) ||
                !isInteger(target)) {
                fail(err, errSize, "command %d: needs ioa, type and target%s", fx->commandCount, "");
                goto done;
            }
            c->ioa = ioa->valueint;
            c->type = (IEC60870_5_TypeID)typeFromName(type->valuestring);
            IEC60870_5_TypeID want = targetTypeOf(c->type);
            if (want == 0) {
                fail(err, errSize, "command %d: unsupported type %s", c->ioa, type->valuestring);
                goto done;
            }
            if (Fixture_findCommand(fx, c->ioa) != NULL || Fixture_findPoint(fx, c->ioa) != NULL) {
                fail(err, errSize, "command %d: duplicate information object address%s", c->ioa, "");
                goto done;
            }
            Point* t = Fixture_findPoint(fx, target->valueint);
            if (t == NULL || t->type != want) {
                fail(err, errSize, "command %d: target must be a point of type %s", c->ioa, typeName(want));
                goto done;
            }
            c->target = (int)(t - fx->points);
            c->reportCause = CS101_COT_RETURN_INFO_REMOTE;
            if (cause != NULL) {
                if (!isInteger(cause) || (cause->valueint != 3 && cause->valueint != 11)) {
                    fail(err, errSize, "command %d: reportCause must be 3 or 11%s", c->ioa, "");
                    goto done;
                }
                c->reportCause = cause->valueint;
            }
            if (sel != NULL) {
                if (!cJSON_IsBool(sel)) {
                    fail(err, errSize, "command %d: selectRequired must be a boolean%s", c->ioa, "");
                    goto done;
                }
                c->selectRequired = cJSON_IsTrue(sel);
            }
            fx->commandCount++;
        }
    }
    ok = true;

done:
    cJSON_Delete(root);
    return ok;
}
