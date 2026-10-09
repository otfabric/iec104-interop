/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <errno.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <time.h>

#include "hal_time.h"

/* Everything the receive thread and the main thread share. */
static struct {
    pthread_mutex_t lock;
    pthread_cond_t changed;
    cJSON* asdus;        /* every ASDU received, in order */
    cJSON* confirmations; /* mirrors of the request that confirm or reject it */
    int asduCount;
    bool startdtCon;
    bool stopdtCon;
    bool closed;
    bool terminated;
    int negativeCot; /* cause of the last refusal, 0 when none */
    bool readAnswered;
    IEC60870_5_TypeID expectType;
    int expectIoa;
    bool expectRead;
    struct FileEvent* fileEvents; /* file transfer ASDUs, for the file operation */
    int fileEventCount;
    int fileEventNext;
} st = { .lock = PTHREAD_MUTEX_INITIALIZER, .changed = PTHREAD_COND_INITIALIZER };

/* One received file transfer ASDU, reduced to what the procedure needs. */
typedef struct FileEvent {
    IEC60870_5_TypeID type;
    int cot;
    bool negative;
    int nos;
    int length;    /* LOF of file ready and section ready */
    int qualifier; /* FRQ, SRQ, LSQ */
    int checksum;
    int size; /* of data */
    uint8_t data[256];
} FileEvent;

/* Called with st.lock held. */
static void pushFileEvent(CS101_ASDU asdu)
{
    IEC60870_5_TypeID type = CS101_ASDU_getTypeID(asdu);
    if (type < F_FR_NA_1 || type > F_SG_NA_1)
        return;
    InformationObject io = CS101_ASDU_getElement(asdu, 0);
    if (io == NULL)
        return;
    st.fileEvents = realloc(st.fileEvents, (size_t)(st.fileEventCount + 1) * sizeof(FileEvent));
    FileEvent* e = &st.fileEvents[st.fileEventCount++];
    memset(e, 0, sizeof(*e));
    e->type = type;
    e->cot = CS101_ASDU_getCOT(asdu);
    e->negative = CS101_ASDU_isNegative(asdu);
    switch (type) {
    case F_FR_NA_1:
        e->length = (int)FileReady_getLengthOfFile((FileReady)io);
        e->qualifier = FileReady_getFRQ((FileReady)io);
        break;
    case F_SR_NA_1:
        e->nos = SectionReady_getNameOfSection((SectionReady)io);
        e->length = (int)SectionReady_getLengthOfSection((SectionReady)io);
        e->qualifier = SectionReady_getSRQ((SectionReady)io);
        break;
    case F_LS_NA_1:
        e->nos = FileLastSegmentOrSection_getNameOfSection((FileLastSegmentOrSection)io);
        e->qualifier = FileLastSegmentOrSection_getLSQ((FileLastSegmentOrSection)io);
        e->checksum = FileLastSegmentOrSection_getCHS((FileLastSegmentOrSection)io);
        break;
    case F_SG_NA_1:
        e->nos = FileSegment_getNameOfSection((FileSegment)io);
        e->size = FileSegment_getLengthOfSegment((FileSegment)io);
        memcpy(e->data, FileSegment_getSegmentData((FileSegment)io), (size_t)e->size);
        break;
    default:
        break;
    }
    InformationObject_destroy(io);
}

static void connectionHandler(void* parameter, CS104_Connection connection, CS104_ConnectionEvent ev)
{
    (void)parameter;
    (void)connection;
    pthread_mutex_lock(&st.lock);
    switch (ev) {
    case CS104_CONNECTION_STARTDT_CON_RECEIVED: st.startdtCon = true; break;
    case CS104_CONNECTION_STOPDT_CON_RECEIVED: st.stopdtCon = true; break;
    case CS104_CONNECTION_CLOSED:
    case CS104_CONNECTION_FAILED: st.closed = true; break;
    default: break;
    }
    pthread_cond_broadcast(&st.changed);
    pthread_mutex_unlock(&st.lock);
}

static bool asduHandler(void* parameter, int address, CS101_ASDU asdu)
{
    (void)parameter;
    (void)address;
    cJSON* j = asduToJson(asdu);
    IEC60870_5_TypeID type = CS101_ASDU_getTypeID(asdu);
    CS101_CauseOfTransmission cot = CS101_ASDU_getCOT(asdu);

    pthread_mutex_lock(&st.lock);
    cJSON_AddItemToArray(st.asdus, j);
    st.asduCount++;
    pushFileEvent(asdu);
    if (st.expectType != 0 && type == st.expectType) {
        bool unknown = cot >= CS101_COT_UNKNOWN_TYPE_ID && cot <= CS101_COT_UNKNOWN_IOA;
        if (cot == CS101_COT_ACTIVATION_CON || cot == CS101_COT_DEACTIVATION_CON || unknown) {
            cJSON* c = cJSON_CreateObject();
            cJSON_AddNumberToObject(c, "cot", cot);
            bool negative = CS101_ASDU_isNegative(asdu) || unknown;
            cJSON_AddBoolToObject(c, "negative", negative);
            cJSON_AddItemToArray(st.confirmations, c);
            if (negative)
                st.negativeCot = cot;
        } else if (cot == CS101_COT_ACTIVATION_TERMINATION) {
            st.terminated = true;
        }
    } else if (st.expectRead && cot == CS101_COT_REQUEST) {
        for (int i = 0; i < CS101_ASDU_getNumberOfElements(asdu); i++) {
            InformationObject io = CS101_ASDU_getElement(asdu, i);
            if (io == NULL)
                break;
            if (InformationObject_getObjectAddress(io) == st.expectIoa)
                st.readAnswered = true;
            InformationObject_destroy(io);
        }
    }
    pthread_cond_broadcast(&st.changed);
    pthread_mutex_unlock(&st.lock);
    return true;
}

static void deadlineAfter(struct timespec* ts, int ms)
{
    clock_gettime(CLOCK_REALTIME, ts);
    ts->tv_sec += ms / 1000;
    ts->tv_nsec += (long)(ms % 1000) * 1000000L;
    if (ts->tv_nsec >= 1000000000L) {
        ts->tv_sec++;
        ts->tv_nsec -= 1000000000L;
    }
}

typedef bool (*Predicate)(int arg);

/* Waits until pred holds, the connection closes or the timeout passes. */
static bool waitFor(Predicate pred, int arg, int timeoutMs)
{
    struct timespec deadline;
    deadlineAfter(&deadline, timeoutMs);
    pthread_mutex_lock(&st.lock);
    bool ok;
    while (!(ok = pred(arg)) && !st.closed) {
        if (pthread_cond_timedwait(&st.changed, &st.lock, &deadline) == ETIMEDOUT) {
            ok = pred(arg);
            break;
        }
    }
    pthread_mutex_unlock(&st.lock);
    return ok;
}

static bool hasStartDt(int a) { (void)a; return st.startdtCon; }
static bool hasStopDt(int a) { (void)a; return st.stopdtCon; }
static bool hasConfirmations(int n) { return cJSON_GetArraySize(st.confirmations) >= n; }
static bool hasTermination(int a) { (void)a; return st.terminated || st.negativeCot != 0; }
static bool hasReadAnswer(int a) { (void)a; return st.readAnswered || st.negativeCot != 0; }
static bool hasAsdus(int n) { return n > 0 && st.asduCount >= n; }
static bool never(int a) { (void)a; return false; }
static bool hasFileEvent(int a) { (void)a; return st.fileEventNext < st.fileEventCount; }

static bool parseBool(const char* s, bool* out)
{
    if (!strcasecmp(s, "true") || !strcmp(s, "1") || !strcasecmp(s, "on")) {
        *out = true;
        return true;
    }
    if (!strcasecmp(s, "false") || !strcmp(s, "0") || !strcasecmp(s, "off")) {
        *out = false;
        return true;
    }
    return false;
}

static bool parseTimeMs(const char* s, uint64_t* out)
{
    struct tm tm;
    memset(&tm, 0, sizeof(tm));
    const char* rest = strptime(s, "%Y-%m-%dT%H:%M:%S", &tm);
    if (rest == NULL)
        return false;
    int ms = 0;
    if (*rest == '.') {
        char* end;
        ms = (int)strtol(rest + 1, &end, 10);
        if (end - (rest + 1) != 3)
            return false;
        rest = end;
    }
    if (strcmp(rest, "Z") != 0)
        return false;
    *out = (uint64_t)timegm(&tm) * 1000 + (uint64_t)ms;
    return true;
}

static InformationObject buildCommand(IEC60870_5_TypeID type, int ioa, const char* value, bool select, int qualifier,
    CP56Time2a ts)
{
    char* end;
    switch (type) {
    case C_SC_NA_1: {
        bool b;
        if (!parseBool(value, &b))
            return NULL;
        return ts ? (InformationObject)SingleCommandWithCP56Time2a_create(NULL, ioa, b, select, qualifier, ts)
                  : (InformationObject)SingleCommand_create(NULL, ioa, b, select, qualifier);
    }
    case C_DC_NA_1:
    case C_RC_NA_1:
    case C_SE_NA_1:
    case C_SE_NB_1: {
        long v = strtol(value, &end, 10);
        if (*value == 0 || *end != 0)
            return NULL;
        if (type == C_DC_NA_1)
            return ts ? (InformationObject)DoubleCommandWithCP56Time2a_create(NULL, ioa, (int)v, select, qualifier, ts)
                      : (InformationObject)DoubleCommand_create(NULL, ioa, (int)v, select, qualifier);
        if (type == C_RC_NA_1)
            return ts ? (InformationObject)StepCommandWithCP56Time2a_create(NULL, ioa, (StepCommandValue)v, select,
                            qualifier, ts)
                      : (InformationObject)StepCommand_create(NULL, ioa, (StepCommandValue)v, select, qualifier);
        if (v < -32768 || v > 32767)
            return NULL;
        if (type == C_SE_NA_1) {
            float f = normalizedFromRaw((int)v);
            return ts ? (InformationObject)SetpointCommandNormalizedWithCP56Time2a_create(NULL, ioa, f, select,
                            qualifier, ts)
                      : (InformationObject)SetpointCommandNormalized_create(NULL, ioa, f, select, qualifier);
        }
        return ts ? (InformationObject)SetpointCommandScaledWithCP56Time2a_create(NULL, ioa, (int)v, select,
                        qualifier, ts)
                  : (InformationObject)SetpointCommandScaled_create(NULL, ioa, (int)v, select, qualifier);
    }
    case C_SE_NC_1: {
        float f = strtof(value, &end);
        if (*value == 0 || *end != 0)
            return NULL;
        return ts ? (InformationObject)SetpointCommandShortWithCP56Time2a_create(NULL, ioa, f, select, qualifier, ts)
                  : (InformationObject)SetpointCommandShort_create(NULL, ioa, f, select, qualifier);
    }
    default:
        return NULL;
    }
}

static const char* errorCode = NULL;
static char errorMessage[200];

static bool failOp(const char* code, const char* fmt, int a)
{
    errorCode = code;
    snprintf(errorMessage, sizeof(errorMessage), fmt, a);
    return false;
}

/* Waits for the n-th confirmation and checks that it is positive. */
static bool awaitConfirmation(int n, int timeoutMs)
{
    if (!waitFor(hasConfirmations, n, timeoutMs))
        return st.closed ? failOp("connection-lost", "connection closed while waiting for confirmation %d", n)
                         : failOp("timeout", "no confirmation %d from the station", n);
    if (st.negativeCot != 0)
        return failOp("negative-confirmation", "the station refused the request (cause %d)", st.negativeCot);
    return true;
}

static bool awaitTermination(int timeoutMs)
{
    if (!waitFor(hasTermination, 0, timeoutMs))
        return st.closed ? failOp("connection-lost", "connection closed while waiting for termination%.0d", 0)
                         : failOp("timeout", "no activation termination from the station%.0d", 0);
    return true;
}

/* ---- the file operation: download in monitor direction ---- */

static bool sendFileCall(CS104_Connection con, int oa, int ca, int ioa, int nof, int nos, int scq)
{
    CS101_ASDU a = CS101_ASDU_create(CS104_Connection_getAppLayerParameters(con), false, CS101_COT_FILE_TRANSFER, oa,
        ca, false, false);
    InformationObject io = (InformationObject)FileCallOrSelect_create(NULL, ioa, (uint16_t)nof, (uint8_t)nos,
        (uint8_t)scq);
    CS101_ASDU_addInformationObject(a, io);
    bool sent = CS104_Connection_sendASDU(con, a);
    InformationObject_destroy(io);
    CS101_ASDU_destroy(a);
    return sent;
}

static bool sendFileAck(CS104_Connection con, int oa, int ca, int ioa, int nof, int nos, int afq)
{
    CS101_ASDU a = CS101_ASDU_create(CS104_Connection_getAppLayerParameters(con), false, CS101_COT_FILE_TRANSFER, oa,
        ca, false, false);
    InformationObject io = (InformationObject)FileACK_create(NULL, ioa, (uint16_t)nof, (uint8_t)nos, (uint8_t)afq);
    CS101_ASDU_addInformationObject(a, io);
    bool sent = CS104_Connection_sendASDU(con, a);
    InformationObject_destroy(io);
    CS101_ASDU_destroy(a);
    return sent;
}

/* Waits for the next file transfer ASDU. */
static bool nextFileEvent(FileEvent* out, int timeoutMs, const char* what)
{
    if (!waitFor(hasFileEvent, 0, timeoutMs)) {
        if (st.closed)
            return failOp("connection-lost", "connection closed during the file transfer%.0d", 0);
        snprintf(errorMessage, sizeof(errorMessage), "the station did not send %s", what);
        errorCode = "timeout";
        return false;
    }
    pthread_mutex_lock(&st.lock);
    *out = st.fileEvents[st.fileEventNext++];
    pthread_mutex_unlock(&st.lock);
    return true;
}

typedef struct {
    uint8_t* data;
    int length;   /* announced by file ready */
    int received;
    int sections;
} Download;

/* Select, call, receive and acknowledge one file. See docs/CONTAINER_CONTRACT.md. */
static bool downloadFile(CS104_Connection con, int oa, int ca, int ioa, int nof, int timeoutMs, Download* d)
{
    FileEvent e;
    sendFileCall(con, oa, ca, ioa, nof, 0, 1 /* select file */);
    if (!nextFileEvent(&e, timeoutMs, "file ready"))
        return false;
    /* A refusal is the mirrored call with an "unknown ..." cause, or a
     * file ready with the negative bit. */
    if (e.type == F_SC_NA_1 || e.negative || (e.type == F_FR_NA_1 && (e.qualifier & 0x80))) {
        st.negativeCot = e.cot;
        return failOp("negative-confirmation", "the station refused the file (cause %d)", e.cot);
    }
    if (e.type != F_FR_NA_1)
        return failOp("failed", "expected file ready, got type %d", e.type);
    d->length = e.length;
    d->data = malloc((size_t)d->length + 1);

    sendFileCall(con, oa, ca, ioa, nof, 0, 2 /* request file */);
    for (;;) {
        if (!nextFileEvent(&e, timeoutMs, "section ready or last section"))
            return false;
        if (e.type == F_LS_NA_1 && (e.qualifier == 1 || e.qualifier == 2)) {
            /* Last section: the checksum covers the whole file. */
            uint8_t sum = 0;
            for (int i = 0; i < d->received; i++)
                sum += d->data[i];
            bool good = sum == e.checksum && d->received == d->length;
            sendFileAck(con, oa, ca, ioa, nof, e.nos, good ? 1 : 2);
            if (!good)
                return failOp("failed", "file checksum or length mismatch after %d octets", d->received);
            return true;
        }
        if (e.type != F_SR_NA_1)
            return failOp("failed", "expected section ready, got type %d", e.type);
        if (e.qualifier & 0x80)
            return failOp("failed", "section %d is not ready", e.nos);
        int nos = e.nos, sectionLength = e.length, sectionStart = d->received;
        d->sections++;
        sendFileCall(con, oa, ca, ioa, nof, nos, 6 /* request section */);
        for (;;) {
            if (!nextFileEvent(&e, timeoutMs, "a segment or last segment"))
                return false;
            if (e.type == F_SG_NA_1) {
                if (d->received + e.size > d->length)
                    return failOp("failed", "more data than the announced %d octets", d->length);
                memcpy(d->data + d->received, e.data, (size_t)e.size);
                d->received += e.size;
                continue;
            }
            if (e.type != F_LS_NA_1 || (e.qualifier != 3 && e.qualifier != 4))
                return failOp("failed", "expected a segment or last segment, got type %d", e.type);
            uint8_t sum = 0;
            for (int i = sectionStart; i < d->received; i++)
                sum += d->data[i];
            bool good = sum == e.checksum && d->received - sectionStart == sectionLength;
            sendFileAck(con, oa, ca, ioa, nof, nos, good ? 3 : 4);
            if (!good)
                return failOp("failed", "checksum or length mismatch in section %d", nos);
            break;
        }
    }
}

int runClient(const char* op, Args* args)
{
    const char* host = argString(args, "--host", NULL);
    int port, ca, oa, timeoutMs, connectTimeoutMs, collectMs;
    if (!argInt(args, "--port", 2404, &port) || !argInt(args, "--common-address", 1, &ca) ||
        !argInt(args, "--originator-address", 0, &oa) || !argInt(args, "--timeout-ms", 5000, &timeoutMs) ||
        !argInt(args, "--connect-timeout-ms", 5000, &connectTimeoutMs) ||
        !argInt(args, "--collect-ms", 0, &collectMs))
        return EXIT_USAGE;
    if (host == NULL) {
        logf_("client: --host is required");
        return EXIT_USAGE;
    }

    /* Operation arguments are parsed before connecting so that a usage
     * error never touches the network. */
    enum {
        OP_CONNECT,
        OP_INTERROGATE,
        OP_COUNTERS,
        OP_READ,
        OP_CLOCK,
        OP_TEST,
        OP_COMMAND,
        OP_MONITOR,
        OP_FILE
    } kind;
    int fileName = 1;
    Download download = { NULL, 0, 0, 0 };
    int qoi = 20, qcc = 5, ioa = 0, holdMs = 0, durationMs = 1000, maxAsdus = 0, qualifier = 0;
    uint64_t clockMs = Hal_getTimeInMs();
    const char* mode = "direct";
    const char* value = NULL;
    IEC60870_5_TypeID commandType = 0;
    bool withTime = false;

    if (!strcmp(op, "connect")) {
        kind = OP_CONNECT;
        if (!argInt(args, "--hold-ms", 0, &holdMs))
            return EXIT_USAGE;
    } else if (!strcmp(op, "interrogate")) {
        kind = OP_INTERROGATE;
        if (!argInt(args, "--qoi", 20, &qoi))
            return EXIT_USAGE;
    } else if (!strcmp(op, "counter-interrogate")) {
        kind = OP_COUNTERS;
        if (!argInt(args, "--qcc", 5, &qcc))
            return EXIT_USAGE;
    } else if (!strcmp(op, "read")) {
        kind = OP_READ;
        if (!argInt(args, "--ioa", 0, &ioa) || ioa < 1) {
            logf_("read: --ioa is required");
            return EXIT_USAGE;
        }
    } else if (!strcmp(op, "clock-sync")) {
        kind = OP_CLOCK;
        const char* t = argString(args, "--time", NULL);
        if (t != NULL && !parseTimeMs(t, &clockMs)) {
            logf_("clock-sync: --time must look like 2026-01-02T03:04:05.678Z");
            return EXIT_USAGE;
        }
    } else if (!strcmp(op, "test-command")) {
        kind = OP_TEST;
    } else if (!strcmp(op, "command")) {
        kind = OP_COMMAND;
        const char* typeArg = argString(args, "--type", "");
        commandType = (IEC60870_5_TypeID)typeFromName(typeArg);
        value = argString(args, "--value", NULL);
        mode = argString(args, "--mode", "direct");
        withTime = argFlag(args, "--with-time");
        if (!argInt(args, "--ioa", 0, &ioa) || !argInt(args, "--qualifier", 0, &qualifier))
            return EXIT_USAGE;
        if (commandType < C_SC_NA_1 || commandType > C_SE_NC_1 || ioa < 1 || value == NULL) {
            logf_("command: --type (C_SC_NA_1, C_DC_NA_1, C_RC_NA_1, C_SE_NA_1, C_SE_NB_1, C_SE_NC_1), --ioa and --value are required");
            return EXIT_USAGE;
        }
        if (strcmp(mode, "direct") && strcmp(mode, "select") && strcmp(mode, "sbo") && strcmp(mode, "cancel")) {
            logf_("command: --mode must be direct, select, sbo or cancel");
            return EXIT_USAGE;
        }
        InformationObject probe = buildCommand(commandType, ioa, value, false, qualifier, NULL);
        if (probe == NULL) {
            logf_("command: --value '%s' is not valid for %s", value, typeArg);
            return EXIT_USAGE;
        }
        InformationObject_destroy(probe);
    } else if (!strcmp(op, "file-get")) {
        kind = OP_FILE;
        if (!argInt(args, "--ioa", 0, &ioa) || !argInt(args, "--name", 1, &fileName))
            return EXIT_USAGE;
        if (ioa < 1 || fileName < 1 || fileName > 65535) {
            logf_("file-get: --ioa is required; --name is 1..65535");
            return EXIT_USAGE;
        }
    } else if (!strcmp(op, "monitor")) {
        kind = OP_MONITOR;
        if (!argInt(args, "--duration-ms", 1000, &durationMs) || !argInt(args, "--max-asdus", 0, &maxAsdus))
            return EXIT_USAGE;
    } else {
        logf_("client: unknown operation %s", op);
        return EXIT_USAGE;
    }

    CS104_Connection con = CS104_Connection_create(host, port);
    struct sCS104_APCIParameters apci = *CS104_Connection_getAPCIParameters(con);
    if (!applyApciArgs(args, &apci))
        return EXIT_USAGE;
    if (argUnknown(args) != NULL) {
        logf_("client %s: unknown argument %s", op, argUnknown(args));
        return EXIT_USAGE;
    }
    CS104_Connection_setAPCIParameters(con, &apci);
    CS104_Connection_setConnectTimeout(con, connectTimeoutMs);
    CS104_Connection_setOriginatorAddress(con, (uint8_t)oa);
    CS104_Connection_setConnectionHandler(con, connectionHandler, NULL);
    CS104_Connection_setASDUReceivedHandler(con, asduHandler, NULL);

    st.asdus = cJSON_CreateArray();
    st.confirmations = cJSON_CreateArray();
    uint64_t begin = Hal_getTimeInMs();
    bool connected = false, ok = false;
    int exitCode = EXIT_OPERATION_FAILED;

    if (!CS104_Connection_connect(con)) {
        failOp("connect-failed", "cannot connect to the station%.0d", 0);
        exitCode = EXIT_CONNECT_FAILED;
        goto report;
    }
    connected = true;
    CS104_Connection_sendStartDT(con);
    if (!waitFor(hasStartDt, 0, timeoutMs)) {
        failOp("startdt-timeout", "no STARTDT con from the station%.0d", 0);
        goto report;
    }

    struct sCP56Time2a now;
    CP56Time2a_createFromMsTimestamp(&now, Hal_getTimeInMs());

    switch (kind) {
    case OP_CONNECT:
        if (holdMs > 0)
            waitFor(never, 0, holdMs);
        CS104_Connection_sendStopDT(con);
        ok = waitFor(hasStopDt, 0, timeoutMs) || failOp("stopdt-timeout", "no STOPDT con from the station%.0d", 0);
        break;

    case OP_INTERROGATE:
        st.expectType = C_IC_NA_1;
        CS104_Connection_sendInterrogationCommand(con, CS101_COT_ACTIVATION, ca, (QualifierOfInterrogation)qoi);
        ok = awaitConfirmation(1, timeoutMs) && awaitTermination(timeoutMs);
        break;

    case OP_COUNTERS:
        st.expectType = C_CI_NA_1;
        CS104_Connection_sendCounterInterrogationCommand(con, CS101_COT_ACTIVATION, ca, (uint8_t)qcc);
        ok = awaitConfirmation(1, timeoutMs) && awaitTermination(timeoutMs);
        break;

    case OP_READ:
        st.expectType = C_RD_NA_1;
        st.expectRead = true;
        st.expectIoa = ioa;
        CS104_Connection_sendReadCommand(con, ca, ioa);
        if (!waitFor(hasReadAnswer, 0, timeoutMs))
            failOp("timeout", "no answer to the read command%.0d", 0);
        else if (st.negativeCot != 0)
            failOp("negative-confirmation", "the station refused the request (cause %d)", st.negativeCot);
        else
            ok = true;
        break;

    case OP_CLOCK: {
        struct sCP56Time2a t;
        CP56Time2a_createFromMsTimestamp(&t, clockMs);
        st.expectType = C_CS_NA_1;
        CS104_Connection_sendClockSyncCommand(con, ca, &t);
        ok = awaitConfirmation(1, timeoutMs);
        break;
    }

    case OP_TEST:
        st.expectType = C_TS_TA_1;
        CS104_Connection_sendTestCommandWithTimestamp(con, ca, 0x4938, &now);
        ok = awaitConfirmation(1, timeoutMs);
        break;

    case OP_COMMAND: {
        CP56Time2a ts = withTime ? &now : NULL;
        st.expectType = withTime ? timeTaggedType(commandType) : commandType;
        int confirmations = 0;
        ok = true;
        if (strcmp(mode, "direct") != 0) {
            InformationObject sel = buildCommand(commandType, ioa, value, true, qualifier, ts);
            CS104_Connection_sendProcessCommandEx(con, CS101_COT_ACTIVATION, ca, sel);
            ok = awaitConfirmation(++confirmations, timeoutMs);
            if (ok && !strcmp(mode, "cancel")) {
                CS104_Connection_sendProcessCommandEx(con, CS101_COT_DEACTIVATION, ca, sel);
                ok = awaitConfirmation(++confirmations, timeoutMs);
            }
            InformationObject_destroy(sel);
        }
        if (ok && (!strcmp(mode, "direct") || !strcmp(mode, "sbo"))) {
            InformationObject exec = buildCommand(commandType, ioa, value, false, qualifier, ts);
            CS104_Connection_sendProcessCommandEx(con, CS101_COT_ACTIVATION, ca, exec);
            InformationObject_destroy(exec);
            ok = awaitConfirmation(++confirmations, timeoutMs) && awaitTermination(timeoutMs);
        }
        break;
    }

    case OP_FILE:
        ok = downloadFile(con, oa, ca, ioa, fileName, timeoutMs, &download);
        break;

    case OP_MONITOR:
        waitFor(hasAsdus, maxAsdus, durationMs);
        ok = !st.closed || failOp("connection-lost", "connection closed while monitoring%.0d", 0);
        break;
    }

    /* Optionally keep listening for what the station sends afterwards. */
    if (ok && collectMs > 0)
        waitFor(never, 0, collectMs);

report:
    if (ok)
        exitCode = EXIT_OK;
    CS104_Connection_destroy(con); /* closes; no callback runs after this */

    cJSON* r = cJSON_CreateObject();
    cJSON_AddStringToObject(r, "schemaVersion", SCHEMA_VERSION);
    cJSON_AddStringToObject(r, "adapter", ADAPTER_NAME);
    cJSON_AddStringToObject(r, "operation", op);
    cJSON_AddBoolToObject(r, "ok", ok);
    if (ok) {
        cJSON_AddNullToObject(r, "error");
    } else {
        cJSON* e = cJSON_AddObjectToObject(r, "error");
        cJSON_AddStringToObject(e, "code", errorCode ? errorCode : "failed");
        cJSON_AddStringToObject(e, "message", errorMessage);
    }
    cJSON_AddBoolToObject(r, "connected", connected);
    cJSON_AddBoolToObject(r, "startdtConfirmed", st.startdtCon);
    if (kind == OP_CONNECT)
        cJSON_AddBoolToObject(r, "stopdtConfirmed", st.stopdtCon);
    cJSON_AddItemToObject(r, "confirmations", st.confirmations);
    cJSON_AddBoolToObject(r, "terminated", st.terminated);
    if (kind == OP_FILE) {
        cJSON* f = cJSON_AddObjectToObject(r, "file");
        char digest[65];
        sha256Hex(download.data ? download.data : (const uint8_t*)"", (size_t)download.received, digest);
        cJSON_AddNumberToObject(f, "ioa", ioa);
        cJSON_AddNumberToObject(f, "name", fileName);
        cJSON_AddNumberToObject(f, "length", download.length);
        cJSON_AddNumberToObject(f, "received", download.received);
        cJSON_AddNumberToObject(f, "sections", download.sections);
        cJSON_AddStringToObject(f, "sha256", digest);
    }
    cJSON_AddItemToObject(r, "asdus", st.asdus);
    cJSON_AddNumberToObject(r, "elapsedMs", (double)(Hal_getTimeInMs() - begin));
    emitJson(r);
    return exitCode;
}
