/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "cs101_file_service.h"
#include "hal_thread.h"
#include "hal_time.h"

static Fixture fx;
/* Guards the fixture state: handlers run on one thread per connection. */
static pthread_mutex_t stateLock = PTHREAD_MUTEX_INITIALIZER;
static volatile sig_atomic_t running = 1;

static void onSignal(int sig)
{
    (void)sig;
    running = 0;
}

static cJSON* event(const char* name)
{
    cJSON* e = cJSON_CreateObject();
    cJSON_AddStringToObject(e, "event", name);
    return e;
}

static void addPeer(cJSON* e, IMasterConnection connection)
{
    char addr[64];
    addr[0] = 0;
    IMasterConnection_getPeerAddress(connection, addr, sizeof(addr));
    cJSON_AddStringToObject(e, "peer", addr);
}

/* Mirrors asdu with one of the "unknown ..." causes and the P/N bit set. */
static void reject(IMasterConnection connection, CS101_ASDU asdu, CS101_CauseOfTransmission cot)
{
    CS101_ASDU_setCOT(asdu, cot);
    CS101_ASDU_setNegative(asdu, true);
    IMasterConnection_sendASDU(connection, asdu);
}

static bool isCAAllowed(void* parameter, int ca)
{
    (void)parameter;
    return ca == fx.commonAddress || ca == 0xFFFF;
}

static bool connectionRequestHandler(void* parameter, const char* ipAddress)
{
    (void)parameter;
    (void)ipAddress;
    return true;
}

static void abandonUpload(IMasterConnection connection);

/* NULL without --redundancy-group. */
static CS104_Slave redundancyGroup;

/*
 * lib60870 keeps one queue for a redundancy group, and the next connection to
 * start sends it, after its STARTDT con. So the event only has to go in there.
 * It says "deactivated" for a STOPDT act only: not for a takeover and not for a
 * connection that ends (iec60870/cs104/cs104_slave.c: MasterConnection_deactivate).
 * That holds the lock of the connection, and enqueueing does not need it.
 */
static void queueEvent(void)
{
    pthread_mutex_lock(&stateLock);
    for (int i = 0; i < fx.pointCount; i++) {
        const Point* p = &fx.points[i];
        if (p->type != M_SP_NA_1)
            continue;
        CS101_ASDU a = CS101_ASDU_create(CS104_Slave_getAppLayerParameters(redundancyGroup), false,
            CS101_COT_SPONTANEOUS, 0, fx.commonAddress, false, false);
        InformationObject io = pointToObject(p, NULL);
        CS101_ASDU_addInformationObject(a, io);
        InformationObject_destroy(io);
        CS104_Slave_enqueueASDU(redundancyGroup, a);
        CS101_ASDU_destroy(a);
        break;
    }
    pthread_mutex_unlock(&stateLock);
}

static void connectionEventHandler(void* parameter, IMasterConnection connection, CS104_PeerConnectionEvent ev)
{
    (void)parameter;
    if (ev == CS104_CON_EVENT_CONNECTION_CLOSED)
        abandonUpload(connection);
    if (ev == CS104_CON_EVENT_DEACTIVATED && redundancyGroup != NULL)
        queueEvent();
    static const char* names[] = { "connection-opened", "connection-closed", "data-transfer-started",
        "data-transfer-stopped" };
    if ((int)ev < 0 || (int)ev > 3)
        return;
    cJSON* e = event(names[ev]);
    addPeer(e, connection);
    emitJson(e);
}

/* Which points an interrogation reports. */
#define SELECT_COUNTERS (-1) /* the integrated totals */
#define SELECT_STATION 0     /* everything else */
/* 1..16: the points of that interrogation group */

static bool isSelected(const Point* p, int selection)
{
    if (selection == SELECT_COUNTERS)
        return p->type == M_IT_NA_1;
    if (selection == SELECT_STATION)
        return p->type != M_IT_NA_1;
    return (p->groups & (1u << (selection - 1))) != 0;
}

/* Sends the selected points in ascending address order, packing consecutive
 * points of one type into one ASDU. */
static void sendPoints(IMasterConnection connection, CS101_CauseOfTransmission cot, int oa, int selection)
{
    CS101_AppLayerParameters params = IMasterConnection_getApplicationLayerParameters(connection);
    CS101_ASDU asdu = NULL;
    IEC60870_5_TypeID current = 0;

    for (int i = 0; i < fx.pointCount; i++) {
        Point* p = &fx.points[i];
        if (!isSelected(p, selection))
            continue;
        InformationObject io = pointToObject(p, NULL);
        if (asdu != NULL && (p->type != current || !CS101_ASDU_addInformationObject(asdu, io))) {
            IMasterConnection_sendASDU(connection, asdu);
            CS101_ASDU_destroy(asdu);
            asdu = NULL;
        }
        if (asdu == NULL) {
            asdu = CS101_ASDU_create(params, false, cot, oa, fx.commonAddress, false, false);
            current = p->type;
            CS101_ASDU_addInformationObject(asdu, io);
        }
        InformationObject_destroy(io);
    }
    if (asdu != NULL) {
        IMasterConnection_sendASDU(connection, asdu);
        CS101_ASDU_destroy(asdu);
    }
}

static bool interrogationHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu, uint8_t qoi)
{
    (void)parameter;
    /* QOI 21..36 are the groups 1..16; a group nothing belongs to is refused. */
    int selection = SELECT_STATION;
    bool accepted = qoi == IEC60870_QOI_STATION;
    if (qoi >= IEC60870_QOI_GROUP_1 && qoi <= IEC60870_QOI_GROUP_16) {
        selection = qoi - IEC60870_QOI_STATION;
        for (int i = 0; i < fx.pointCount && !accepted; i++)
            accepted = isSelected(&fx.points[i], selection);
    }
    cJSON* e = event("interrogation");
    cJSON_AddNumberToObject(e, "commonAddress", CS101_ASDU_getCA(asdu));
    cJSON_AddNumberToObject(e, "qoi", qoi);
    cJSON_AddBoolToObject(e, "accepted", accepted);
    emitJson(e);

    /* A broadcast request is answered with the station's own address. */
    CS101_ASDU_setCA(asdu, fx.commonAddress);
    if (!accepted) {
        IMasterConnection_sendACT_CON(connection, asdu, true);
        return true;
    }
    IMasterConnection_sendACT_CON(connection, asdu, false);
    pthread_mutex_lock(&stateLock);
    /* The cause of an interrogation's answers is its QOI: 20, or 21..36. */
    sendPoints(connection, (CS101_CauseOfTransmission)qoi, CS101_ASDU_getOA(asdu), selection);
    pthread_mutex_unlock(&stateLock);
    IMasterConnection_sendACT_TERM(connection, asdu);
    return true;
}

static bool counterInterrogationHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu,
    QualifierOfCIC qcc)
{
    (void)parameter;
    /* Only "general request counter" with "read" (no freeze, no reset). */
    bool accepted = qcc == (IEC60870_QCC_RQT_GENERAL | IEC60870_QCC_FRZ_READ);
    cJSON* e = event("counter-interrogation");
    cJSON_AddNumberToObject(e, "commonAddress", CS101_ASDU_getCA(asdu));
    cJSON_AddNumberToObject(e, "qcc", qcc);
    cJSON_AddBoolToObject(e, "accepted", accepted);
    emitJson(e);

    CS101_ASDU_setCA(asdu, fx.commonAddress);
    if (!accepted) {
        IMasterConnection_sendACT_CON(connection, asdu, true);
        return true;
    }
    IMasterConnection_sendACT_CON(connection, asdu, false);
    pthread_mutex_lock(&stateLock);
    sendPoints(connection, CS101_COT_REQUESTED_BY_GENERAL_COUNTER, CS101_ASDU_getOA(asdu), SELECT_COUNTERS);
    pthread_mutex_unlock(&stateLock);
    IMasterConnection_sendACT_TERM(connection, asdu);
    return true;
}

static bool readHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu, int ioa)
{
    (void)parameter;
    pthread_mutex_lock(&stateLock);
    Point* p = Fixture_findPoint(&fx, ioa);
    InformationObject io = p ? pointToObject(p, NULL) : NULL;
    pthread_mutex_unlock(&stateLock);

    cJSON* e = event("read");
    cJSON_AddNumberToObject(e, "ioa", ioa);
    cJSON_AddBoolToObject(e, "accepted", io != NULL);
    emitJson(e);

    if (io == NULL) {
        reject(connection, asdu, CS101_COT_UNKNOWN_IOA);
        return true;
    }
    CS101_ASDU reply = CS101_ASDU_create(IMasterConnection_getApplicationLayerParameters(connection), false,
        CS101_COT_REQUEST, CS101_ASDU_getOA(asdu), fx.commonAddress, false, false);
    CS101_ASDU_addInformationObject(reply, io);
    IMasterConnection_sendASDU(connection, reply);
    CS101_ASDU_destroy(reply);
    InformationObject_destroy(io);
    return true;
}

static bool clockSyncHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu, CP56Time2a newTime)
{
    (void)parameter;
    (void)connection;
    (void)asdu;
    char buf[40];
    formatTime(CP56Time2a_toMsTimestamp(newTime), buf, sizeof(buf));
    cJSON* e = event("clock-sync");
    cJSON_AddStringToObject(e, "time", buf);
    emitJson(e);
    return true; /* lib60870 sends the activation confirmation */
}

/* Decodes the command object of asdu. Returns false when the value is one
 * the command type does not permit. */
static bool decodeCommand(InformationObject io, IEC60870_5_TypeID type, double* value, bool* select, cJSON* e)
{
    switch (type) {
    case C_SC_NA_1:
        *value = SingleCommand_getState((SingleCommand)io) ? 1 : 0;
        *select = SingleCommand_isSelect((SingleCommand)io);
        cJSON_AddBoolToObject(e, "value", *value != 0);
        return true;
    case C_DC_NA_1:
        *value = DoubleCommand_getState((DoubleCommand)io);
        *select = DoubleCommand_isSelect((DoubleCommand)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return *value == 1 || *value == 2;
    case C_RC_NA_1:
        *value = StepCommand_getState((StepCommand)io);
        *select = StepCommand_isSelect((StepCommand)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return *value == IEC60870_STEP_LOWER || *value == IEC60870_STEP_HIGHER;
    case C_SE_NA_1:
        *value = normalizedToRaw(SetpointCommandNormalized_getValue((SetpointCommandNormalized)io));
        *select = SetpointCommandNormalized_isSelect((SetpointCommandNormalized)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return true;
    case C_SE_NB_1:
        *value = SetpointCommandScaled_getValue((SetpointCommandScaled)io);
        *select = SetpointCommandScaled_isSelect((SetpointCommandScaled)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return true;
    case C_SE_NC_1:
        *value = (double)SetpointCommandShort_getValue((SetpointCommandShort)io);
        *select = SetpointCommandShort_isSelect((SetpointCommandShort)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return true;
    default:
        return false;
    }
}

/* Applies an executed command to its target. Returns false when the target
 * cannot take the value (a step position at its limit). */
static bool applyCommand(Command* c, double value)
{
    Point* t = &fx.points[c->target];
    if (c->type == C_RC_NA_1) {
        double next = t->value + (value == IEC60870_STEP_HIGHER ? 1 : -1);
        if (next < -64 || next > 63)
            return false;
        t->value = next;
        return true;
    }
    t->value = value;
    return true;
}

static bool asduHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu)
{
    (void)parameter;
    IEC60870_5_TypeID wire = CS101_ASDU_getTypeID(asdu);
    IEC60870_5_TypeID type = baseType(wire);
    if (type < C_SC_NA_1 || type > C_SE_NC_1)
        return false; /* lib60870 answers "unknown type identification" */

    CS101_CauseOfTransmission cot = CS101_ASDU_getCOT(asdu);
    if (cot != CS101_COT_ACTIVATION && cot != CS101_COT_DEACTIVATION) {
        reject(connection, asdu, CS101_COT_UNKNOWN_COT);
        return true;
    }
    InformationObject io = CS101_ASDU_getElement(asdu, 0);
    if (io == NULL)
        return false;
    int ioa = InformationObject_getObjectAddress(io);

    cJSON* e = event("command");
    cJSON_AddStringToObject(e, "type", typeName(wire));
    cJSON_AddNumberToObject(e, "ioa", ioa);
    cJSON_AddNumberToObject(e, "cot", cot);

    pthread_mutex_lock(&stateLock);
    Command* c = Fixture_findCommand(&fx, ioa);
    if (c == NULL || c->type != type) {
        pthread_mutex_unlock(&stateLock);
        InformationObject_destroy(io);
        cJSON_AddStringToObject(e, "outcome", "unknown-ioa");
        emitJson(e);
        reject(connection, asdu, CS101_COT_UNKNOWN_IOA);
        return true;
    }

    double value = 0;
    bool select = false;
    bool valid = decodeCommand(io, type, &value, &select, e);
    InformationObject_destroy(io);
    cJSON_AddBoolToObject(e, "select", select);
    CS101_ASDU_setCA(asdu, fx.commonAddress);

    const char* outcome;
    InformationObject report = NULL;
    if (cot == CS101_COT_DEACTIVATION) {
        c->selected = false;
        outcome = "deactivated";
    } else if (!valid) {
        outcome = "rejected";
    } else if (select) {
        c->selected = true;
        outcome = "selected";
    } else if (c->selectRequired && !c->selected) {
        outcome = "rejected";
    } else {
        c->selected = false;
        if (applyCommand(c, value)) {
            struct sCP56Time2a now;
            CP56Time2a_createFromMsTimestamp(&now, Hal_getTimeInMs());
            report = pointToObject(&fx.points[c->target], &now);
            outcome = "executed";
        } else {
            outcome = "rejected";
        }
    }
    int reportCause = c->reportCause;
    pthread_mutex_unlock(&stateLock);

    cJSON_AddStringToObject(e, "outcome", outcome);
    emitJson(e);

    if (strcmp(outcome, "deactivated") == 0) {
        CS101_ASDU_setCOT(asdu, CS101_COT_DEACTIVATION_CON);
        CS101_ASDU_setNegative(asdu, false);
        IMasterConnection_sendASDU(connection, asdu);
    } else if (strcmp(outcome, "rejected") == 0) {
        IMasterConnection_sendACT_CON(connection, asdu, true);
    } else {
        IMasterConnection_sendACT_CON(connection, asdu, false);
        if (report != NULL) {
            /* Confirmation, then the new state of the target, then termination. */
            CS101_ASDU r = CS101_ASDU_create(IMasterConnection_getApplicationLayerParameters(connection), false,
                (CS101_CauseOfTransmission)reportCause, 0, fx.commonAddress, false, false);
            CS101_ASDU_addInformationObject(r, report);
            IMasterConnection_sendASDU(connection, r);
            CS101_ASDU_destroy(r);
            InformationObject_destroy(report);
            IMasterConnection_sendACT_TERM(connection, asdu);
        }
    }
    return true;
}

/* ---- files: served by lib60870's own file server ---- */

/* The files of the fixture and the places to upload to, by address. */
static FileSpec* catalog;
static int catalogCount;
static struct sCS101_IFileProvider* providers;
static CS101_FileServer fileServer;

static FileSpec* specOf(CS101_IFileProvider self) { return (FileSpec*)self->object; }

/* Called with stateLock held. */
static bool isAvailable(const FileSpec* f) { return f->maxSize == 0 || f->stored; }

static uint64_t fileDate(CS101_IFileProvider self)
{
    (void)self;
    return 0;
}

static int fileSize(CS101_IFileProvider self)
{
    pthread_mutex_lock(&stateLock);
    int size = specOf(self)->size;
    pthread_mutex_unlock(&stateLock);
    return size;
}

/* sectionNumber counts from 0; a size of 0 says there is no such section. */
static int sectionSize(CS101_IFileProvider self, int sectionNumber)
{
    FileSpec* f = specOf(self);
    pthread_mutex_lock(&stateLock);
    int left = f->size - sectionNumber * f->sectionSize;
    int size = left < f->sectionSize ? left : f->sectionSize;
    pthread_mutex_unlock(&stateLock);
    return sectionNumber < 0 || left <= 0 ? 0 : size;
}

static bool segmentData(CS101_IFileProvider self, int sectionNumber, int offset, int size, uint8_t* data)
{
    FileSpec* f = specOf(self);
    pthread_mutex_lock(&stateLock);
    int base = sectionNumber * f->sectionSize + offset;
    bool ok = base >= 0 && base + size <= f->size;
    for (int i = 0; ok && i < size; i++)
        data[i] = f->data != NULL ? f->data[base + i] : fileOctet(f->ioa, base + i);
    pthread_mutex_unlock(&stateLock);
    return ok;
}

static void transferComplete(CS101_IFileProvider self, bool success)
{
    cJSON* e = event("file-transfer");
    cJSON_AddNumberToObject(e, "ioa", specOf(self)->ioa);
    cJSON_AddNumberToObject(e, "name", specOf(self)->name);
    cJSON_AddBoolToObject(e, "success", success);
    emitJson(e);
}

static CS101_IFileProvider nextFile(void* parameter, CS101_IFileProvider after)
{
    (void)parameter;
    CS101_IFileProvider next = NULL;
    pthread_mutex_lock(&stateLock);
    for (int i = after == NULL ? 0 : (int)(after - providers) + 1; i < catalogCount && next == NULL; i++)
        if (isAvailable(&catalog[i]))
            next = &providers[i];
    pthread_mutex_unlock(&stateLock);
    return next;
}

/* errCode 1: unknown common address, 2: unknown information object address. */
static CS101_IFileProvider getFile(void* parameter, int ca, int ioa, uint16_t nof, int* errCode)
{
    (void)parameter;
    if (ca != fx.commonAddress) {
        *errCode = 1;
        return NULL;
    }
    CS101_IFileProvider file = NULL;
    pthread_mutex_lock(&stateLock);
    for (int i = 0; i < catalogCount && file == NULL; i++)
        if (catalog[i].ioa == ioa && catalog[i].name == nof && isAvailable(&catalog[i]))
            file = &providers[i];
    pthread_mutex_unlock(&stateLock);
    if (file == NULL)
        *errCode = 2;
    return file;
}

static int compareFiles(const void* a, const void* b)
{
    return ((const FileSpec*)a)->ioa - ((const FileSpec*)b)->ioa;
}

static void buildCatalog(void)
{
    catalogCount = fx.fileCount + fx.uploadCount;
    catalog = calloc((size_t)catalogCount + 1, sizeof(*catalog));
    providers = calloc((size_t)catalogCount + 1, sizeof(*providers));
    for (int i = 0; i < fx.fileCount; i++)
        catalog[i] = fx.files[i];
    for (int i = 0; i < fx.uploadCount; i++) {
        FileSpec* f = &catalog[fx.fileCount + i];
        f->ioa = fx.uploads[i].ioa;
        f->name = fx.uploads[i].name;
        f->maxSize = fx.uploads[i].maxSize;
        f->sectionSize = 2048;
    }
    qsort(catalog, (size_t)catalogCount, sizeof(*catalog), compareFiles);
    for (int i = 0; i < catalogCount; i++) {
        providers[i].ca = fx.commonAddress;
        providers[i].ioa = catalog[i].ioa;
        providers[i].nof = (uint8_t)catalog[i].name;
        providers[i].object = &catalog[i];
        providers[i].getFileDate = fileDate;
        providers[i].getFileSize = fileSize;
        providers[i].getSectionSize = sectionSize;
        providers[i].getSegmentData = segmentData;
        providers[i].transferComplete = transferComplete;
    }
}

/* ---- file upload: lib60870's file server runs the procedure ---- */

/*
 * It keeps one transfer per station and hands the receiver the segments, but it
 * acknowledges every section and every file without looking at them
 * (file-service/file_server.c: F_LS_NA_1). So the plugin in front of it, below,
 * keeps the checks of docs/FIXTURES.md, and only passes on what is correct.
 */
static struct {
    pthread_mutex_t lock;
    bool active;
    IMasterConnection announcedBy; /* of the file ready being handled */
    IMasterConnection connection;
    FileSpec* spec;
    int length; /* announced by file ready */
    uint8_t* data;
    int received;
    int sectionBase;
    int sectionLength; /* announced by section ready */
    bool overflow;     /* more octets arrived than were announced */
} upload = { .lock = PTHREAD_MUTEX_INITIALIZER };

/* Called with upload.lock held. */
static void endUpload(bool success)
{
    char digest[65];
    sha256Hex(upload.data, (size_t)upload.received, digest);
    if (success) {
        pthread_mutex_lock(&stateLock);
        free((void*)upload.spec->data);
        upload.spec->data = upload.data;
        upload.spec->size = upload.received;
        upload.spec->stored = true;
        pthread_mutex_unlock(&stateLock);
    } else {
        free(upload.data);
    }
    upload.data = NULL;
    upload.active = false;

    cJSON* e = event("file-received");
    cJSON_AddNumberToObject(e, "ioa", upload.spec->ioa);
    cJSON_AddNumberToObject(e, "name", upload.spec->name);
    cJSON_AddNumberToObject(e, "length", upload.received);
    cJSON_AddStringToObject(e, "sha256", digest);
    cJSON_AddBoolToObject(e, "success", success);
    emitJson(e);
}

static void abandonUpload(IMasterConnection connection)
{
    pthread_mutex_lock(&upload.lock);
    if (upload.active && upload.connection == connection)
        endUpload(false);
    pthread_mutex_unlock(&upload.lock);
}

static void uploadSegment(CS101_IFileReceiver self, uint8_t sectionName, int offset, int size, uint8_t* data)
{
    (void)self;
    (void)sectionName;
    pthread_mutex_lock(&upload.lock);
    if (upload.active) {
        int at = upload.sectionBase + offset;
        if (at + size > upload.length) {
            upload.overflow = true;
        } else {
            memcpy(upload.data + at, data, (size_t)size);
            upload.received = at + size;
        }
    }
    pthread_mutex_unlock(&upload.lock);
}

static void uploadFinished(CS101_IFileReceiver self, CS101_FileErrorCode result)
{
    (void)self;
    pthread_mutex_lock(&upload.lock);
    if (upload.active)
        endUpload(result == CS101_FILE_ERROR_SUCCESS);
    pthread_mutex_unlock(&upload.lock);
}

static struct sCS101_IFileReceiver uploadReceiver = { NULL, uploadFinished, uploadSegment };

/* errCode 1: unknown common address, 2: unknown information object address.
 * lib60870 mirrors the file ready with that cause. */
static CS101_IFileReceiver fileReady(void* parameter, int ca, int ioa, uint16_t nof, int length, int* errCode)
{
    (void)parameter;
    if (ca != fx.commonAddress) {
        *errCode = 1;
        return NULL;
    }
    FileSpec* spec = NULL;
    for (int i = 0; i < catalogCount && spec == NULL; i++)
        if (catalog[i].maxSize > 0 && catalog[i].ioa == ioa && catalog[i].name == nof && length >= 1 &&
            length <= catalog[i].maxSize)
            spec = &catalog[i];
    if (spec == NULL) {
        *errCode = 2;
        return NULL;
    }
    pthread_mutex_lock(&upload.lock);
    if (upload.active)
        endUpload(false);
    upload.active = true;
    upload.connection = upload.announcedBy;
    upload.spec = spec;
    upload.length = length;
    upload.data = malloc((size_t)length);
    upload.received = 0;
    upload.sectionBase = 0;
    upload.sectionLength = 0;
    upload.overflow = false;
    pthread_mutex_unlock(&upload.lock);
    return &uploadReceiver;
}

static uint8_t checksum(const uint8_t* data, int size)
{
    uint8_t sum = 0;
    for (int i = 0; i < size; i++)
        sum += data[i];
    return sum;
}

/* Answers the F_LS_NA_1 asdu with a negative acknowledgement, and ends the
 * transfer in the file server the way a controlling station aborts one. */
static void refuseUpload(IMasterConnection connection, CS101_ASDU asdu, FileLastSegmentOrSection last, int afq)
{
    int ioa = InformationObject_getObjectAddress((InformationObject)last);
    uint16_t nof = FileLastSegmentOrSection_getNOF(last);
    uint8_t nos = FileLastSegmentOrSection_getNameOfSection(last);
    CS101_AppLayerParameters params = IMasterConnection_getApplicationLayerParameters(connection);

    CS101_ASDU ack = CS101_ASDU_create(params, false, CS101_COT_FILE_TRANSFER, CS101_ASDU_getOA(asdu),
        fx.commonAddress, false, false);
    InformationObject io = (InformationObject)FileACK_create(NULL, ioa, nof, nos, (uint8_t)afq);
    CS101_ASDU_addInformationObject(ack, io);
    IMasterConnection_sendASDU(connection, ack);
    InformationObject_destroy(io);
    CS101_ASDU_destroy(ack);

    CS101_ASDU abort = CS101_ASDU_create(params, false, CS101_COT_FILE_TRANSFER, CS101_ASDU_getOA(asdu),
        fx.commonAddress, false, false);
    io = (InformationObject)FileLastSegmentOrSection_create(NULL, ioa, nof, nos, 2 /* with deactivation */, 0);
    CS101_ASDU_addInformationObject(abort, io);
    CS101_SlavePlugin plugin = CS101_FileServer_getSlavePlugin(fileServer);
    plugin->handleAsdu(plugin->parameter, connection, abort);
    InformationObject_destroy(io);
    CS101_ASDU_destroy(abort);
}

/* ---- directory: lib60870's file server swallows the call ---- */

/* file-service/file_server.c: "TODO send directory", and the ASDU counts as
 * handled. So this has to come first, and answers it. */
static void answerDirectory(IMasterConnection connection, CS101_ASDU asdu)
{
    CS101_ASDU_setCA(asdu, fx.commonAddress);
    InformationObject call = CS101_ASDU_getElement(asdu, 0);
    int scq = call != NULL ? FileCallOrSelect_getSCQ((FileCallOrSelect)call) : -1;
    if (call != NULL)
        InformationObject_destroy(call);

    pthread_mutex_lock(&stateLock);
    int last = -1;
    for (int i = 0; i < catalogCount; i++)
        if (isAvailable(&catalog[i]))
            last = i;
    if (scq != 0 || last < 0) {
        pthread_mutex_unlock(&stateLock);
        reject(connection, asdu, CS101_COT_UNKNOWN_IOA);
        return;
    }

    struct sCP56Time2a now;
    CP56Time2a_createFromMsTimestamp(&now, Hal_getTimeInMs());
    CS101_AppLayerParameters params = IMasterConnection_getApplicationLayerParameters(connection);
    CS101_ASDU a = NULL;
    for (int i = 0; i <= last; i++) {
        if (!isAvailable(&catalog[i]))
            continue;
        InformationObject io = (InformationObject)FileDirectory_create(NULL, catalog[i].ioa,
            (uint16_t)catalog[i].name, (uint32_t)catalog[i].size, i == last ? CS101_SOF_LFD : 0, &now);
        if (a != NULL && !CS101_ASDU_addInformationObject(a, io)) {
            IMasterConnection_sendASDU(connection, a);
            CS101_ASDU_destroy(a);
            a = NULL;
        }
        if (a == NULL) {
            a = CS101_ASDU_create(params, false, CS101_COT_REQUEST, CS101_ASDU_getOA(asdu), fx.commonAddress, false,
                false);
            CS101_ASDU_addInformationObject(a, io);
        }
        InformationObject_destroy(io);
    }
    IMasterConnection_sendASDU(connection, a);
    CS101_ASDU_destroy(a);
    pthread_mutex_unlock(&stateLock);
}

/* ---- the plugin in front of the file server ---- */

/* lib60870 calls the plugins in the order they were added, before any handler
 * of the application (iec60870/cs104/cs104_slave.c: handleASDU). */
static CS101_SlavePlugin_Result fileFront(void* parameter, IMasterConnection connection, CS101_ASDU asdu)
{
    (void)parameter;
    IEC60870_5_TypeID type = CS101_ASDU_getTypeID(asdu);
    if (type == F_SC_NA_1 && CS101_ASDU_getCOT(asdu) == CS101_COT_REQUEST) {
        answerDirectory(connection, asdu);
        return CS101_PLUGIN_RESULT_HANDLED;
    }
    if (fileServer == NULL || (type != F_FR_NA_1 && type != F_SR_NA_1 && type != F_SG_NA_1 && type != F_LS_NA_1))
        return CS101_PLUGIN_RESULT_NOT_HANDLED;

    /* The file server answers with the address it got: a broadcast must not. */
    CS101_ASDU_setCA(asdu, fx.commonAddress);

    InformationObject io = NULL;
    if (type == F_SR_NA_1 || type == F_LS_NA_1) {
        io = CS101_ASDU_getElement(asdu, 0);
        if (io == NULL)
            return CS101_PLUGIN_RESULT_NOT_HANDLED;
    }
    CS101_SlavePlugin_Result result = CS101_PLUGIN_RESULT_NOT_HANDLED;
    int refuse = 0;
    pthread_mutex_lock(&upload.lock);
    if (type == F_FR_NA_1) {
        upload.announcedBy = connection;
    } else if (upload.active && upload.connection == connection && type == F_SR_NA_1) {
        upload.sectionBase = upload.received;
        upload.sectionLength = (int)SectionReady_getLengthOfSection((SectionReady)io);
    } else if (upload.active && upload.connection == connection && type == F_LS_NA_1) {
        FileLastSegmentOrSection last = (FileLastSegmentOrSection)io;
        int lsq = FileLastSegmentOrSection_getLSQ(last);
        uint8_t chs = FileLastSegmentOrSection_getCHS(last);
        if (lsq == 3) {
            int size = upload.received - upload.sectionBase;
            if (upload.overflow || size != upload.sectionLength ||
                checksum(upload.data + upload.sectionBase, size) != chs)
                refuse = 4;
        } else if (lsq == 1) {
            if (upload.overflow || upload.received != upload.length || checksum(upload.data, upload.received) != chs)
                refuse = 2;
        }
    }
    pthread_mutex_unlock(&upload.lock);
    if (refuse != 0) {
        refuseUpload(connection, asdu, (FileLastSegmentOrSection)io, refuse);
        result = CS101_PLUGIN_RESULT_HANDLED;
    }
    if (io != NULL)
        InformationObject_destroy(io);
    return result;
}

static struct sCS101_SlavePlugin filePlugin = { .handleAsdu = fileFront };

int runServer(Args* args)
{
    const char* fixturePath = argString(args, "--fixture", DEFAULT_FIXTURE);
    const char* bindAddress = argString(args, "--bind-address", "0.0.0.0");
    const char* readyFile = argString(args, "--ready-file", DEFAULT_READY_FILE);
    int port;
    if (!argInt(args, "--port", 2404, &port) || port < 1 || port > 65535)
        return EXIT_USAGE;

    TLSConfiguration tls;
    if (!tlsFromArgs(args, &tls))
        return EXIT_USAGE;

    bool group = argFlag(args, "--redundancy-group");
    CS104_Slave slave = tls ? CS104_Slave_createSecure(1024, 1024, tls) : CS104_Slave_create(1024, 1024);
    if (!applyApciArgs(args, CS104_Slave_getConnectionParameters(slave)))
        return EXIT_USAGE;
    if (argUnknown(args) != NULL) {
        logf_("server: unknown argument %s", argUnknown(args));
        return EXIT_USAGE;
    }

    char err[256];
    if (!Fixture_load(fixturePath, &fx, err, sizeof(err))) {
        logf_("fixture %s: %s", fixturePath, err);
        return EXIT_FIXTURE_INVALID;
    }
    unlink(readyFile);

    CS104_Slave_setLocalAddress(slave, bindAddress);
    CS104_Slave_setLocalPort(slave, port);
    /* Without --redundancy-group, every connection is its own redundancy group:
     * independent sessions. */
    CS104_Slave_setServerMode(slave, group ? CS104_MODE_SINGLE_REDUNDANCY_GROUP : CS104_MODE_CONNECTION_IS_REDUNDANCY_GROUP);
    redundancyGroup = group ? slave : NULL;
    CS104_Slave_setMaxOpenConnections(slave, 16);
    CS104_Slave_setAllowedCAHandler(slave, isCAAllowed, NULL);
    CS104_Slave_setConnectionRequestHandler(slave, connectionRequestHandler, NULL);
    CS104_Slave_setConnectionEventHandler(slave, connectionEventHandler, NULL);
    CS104_Slave_setInterrogationHandler(slave, interrogationHandler, NULL);
    CS104_Slave_setCounterInterrogationHandler(slave, counterInterrogationHandler, NULL);
    CS104_Slave_setReadHandler(slave, readHandler, NULL);
    CS104_Slave_setClockSyncHandler(slave, clockSyncHandler, NULL);
    CS104_Slave_setASDUHandler(slave, asduHandler, NULL);

    struct sCS101_FilesAvailable filesAvailable = { nextFile, getFile, NULL };
    buildCatalog();
    CS104_Slave_addPlugin(slave, &filePlugin);
    if (catalogCount > 0) {
        fileServer = CS101_FileServer_create(CS104_Slave_getAppLayerParameters(slave));
        CS101_FileServer_setFilesAvailableIfc(fileServer, &filesAvailable);
        CS101_FileServer_setFileReadyHandler(fileServer, fileReady, NULL);
        CS104_Slave_addPlugin(slave, CS101_FileServer_getSlavePlugin(fileServer));
    }

    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = onSignal;
    sigaction(SIGTERM, &sa, NULL);
    sigaction(SIGINT, &sa, NULL);

    CS104_Slave_start(slave);
    if (!CS104_Slave_isRunning(slave)) {
        logf_("cannot listen on %s:%d", bindAddress, port);
        CS104_Slave_destroy(slave);
        return EXIT_OPERATION_FAILED;
    }

    FILE* f = fopen(readyFile, "w");
    if (f != NULL) {
        fputs("ready\n", f);
        fclose(f);
    } else {
        logf_("warning: cannot write ready file %s", readyFile);
    }
    char address[96];
    snprintf(address, sizeof(address), "%s:%d", bindAddress, port);
    cJSON* e = event("ready");
    cJSON_AddStringToObject(e, "adapter", ADAPTER_NAME);
    cJSON_AddStringToObject(e, "address", address);
    cJSON_AddStringToObject(e, "fixture", fx.name);
    cJSON_AddNumberToObject(e, "commonAddress", fx.commonAddress);
    if (tls)
        cJSON_AddTrueToObject(e, "tls");
    if (group)
        cJSON_AddTrueToObject(e, "redundancyGroup");
    emitJson(e);

    while (running && CS104_Slave_isRunning(slave))
        Thread_sleep(50);

    unlink(readyFile);
    CS104_Slave_stop(slave);
    CS104_Slave_destroy(slave);
    if (fileServer != NULL)
        CS101_FileServer_destroy(fileServer);
    emitJson(event("stopped"));
    return EXIT_OK;
}
