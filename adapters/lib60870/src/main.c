/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <stdio.h>
#include <string.h>

static const char USAGE[] =
    "usage: iec104-interop <command>\n"
    "\n"
    "  server [--fixture PATH] [--bind-address ADDR] [--port N] [--ready-file PATH] [APCI]\n"
    "  client <operation> --host HOST [--port N] [--common-address N] [--originator-address N]\n"
    "         [--timeout-ms N] [--connect-timeout-ms N] [--collect-ms N] [APCI] [operation flags]\n"
    "      connect             [--hold-ms N]\n"
    "      interrogate         [--qoi N]\n"
    "      counter-interrogate [--qcc N]\n"
    "      read                --ioa N\n"
    "      clock-sync          [--time 2026-01-02T03:04:05.678Z]\n"
    "      test-command\n"
    "      command             --type T --ioa N --value V [--mode direct|select|sbo|cancel]\n"
    "                          [--qualifier N] [--with-time]\n"
    "      monitor             [--duration-ms N] [--max-asdus N]\n"
    "      file-get            --ioa N [--name N]\n"
    "  print-capabilities\n"
    "  print-fixture [NAME]\n"
    "  version\n"
    "\n"
    "  APCI: [--k N] [--w N] [--t0 S] [--t1 S] [--t2 S] [--t3 S]\n"
    "\n"
    "See docs/CONTAINER_CONTRACT.md in https://github.com/otfabric/iec104-interop\n";

int printCapabilities(void)
{
    static const char* pointTypes[] = { "M_SP_NA_1", "M_DP_NA_1", "M_ST_NA_1", "M_BO_NA_1", "M_ME_NA_1", "M_ME_NB_1",
        "M_ME_NC_1", "M_IT_NA_1" };
    static const char* commandTypes[] = { "C_SC_NA_1", "C_DC_NA_1", "C_RC_NA_1", "C_SE_NA_1", "C_SE_NB_1",
        "C_SE_NC_1" };
    static const char* operations[] = { "connect", "interrogate", "counter-interrogate", "read", "clock-sync",
        "test-command", "command", "monitor", "file-get" };

    cJSON* c = cJSON_CreateObject();
    cJSON_AddStringToObject(c, "schemaVersion", SCHEMA_VERSION);
    cJSON_AddStringToObject(c, "adapter", ADAPTER_NAME);
    cJSON_AddStringToObject(c, "adapterVersion", ADAPTER_VERSION);
    cJSON_AddStringToObject(c, "protocol", "iec60870-5-104");
    cJSON* up = cJSON_AddObjectToObject(c, "upstream");
    cJSON_AddStringToObject(up, "name", "lib60870-C");
    cJSON_AddStringToObject(up, "version", UPSTREAM_VERSION);
    cJSON_AddStringToObject(up, "revision", UPSTREAM_REVISION);
    cJSON_AddStringToObject(up, "license", "GPL-3.0");
    cJSON_AddStringToObject(up, "language", "C");
    /* Not a wrapper around another adapter's protocol engine. */
    cJSON_AddTrueToObject(up, "independentEngine");

    cJSON* roles = cJSON_AddObjectToObject(c, "roles");
    cJSON_AddTrueToObject(roles, "server");
    cJSON_AddTrueToObject(roles, "client");

    cJSON* f = cJSON_AddObjectToObject(c, "features");
    cJSON_AddTrueToObject(f, "generalInterrogation");
    cJSON_AddFalseToObject(f, "groupInterrogation");
    cJSON_AddTrueToObject(f, "counterInterrogation");
    cJSON_AddTrueToObject(f, "read");
    cJSON_AddTrueToObject(f, "clockSync");
    cJSON_AddTrueToObject(f, "testCommand");
    cJSON_AddTrueToObject(f, "directCommands");
    cJSON_AddTrueToObject(f, "selectBeforeOperate");
    cJSON_AddTrueToObject(f, "commandDeactivation");
    cJSON_AddTrueToObject(f, "timeTaggedCommands");
    cJSON_AddTrueToObject(f, "spontaneousOnCommand");
    cJSON_AddTrueToObject(f, "apciParameters");
    cJSON_AddTrueToObject(f, "multipleConnections");
    /* The server reports STARTDT and STOPDT as events. */
    cJSON_AddTrueToObject(f, "dataTransferEvents");
    cJSON_AddFalseToObject(f, "pointQualityOnBitstring");
    cJSON_AddFalseToObject(f, "tls");
    cJSON_AddTrueToObject(f, "fileTransfer");
    cJSON_AddTrueToObject(f, "fileServer");
    cJSON_AddTrueToObject(f, "fileClient");

    cJSON_AddItemToObject(c, "pointTypes", cJSON_CreateStringArray(pointTypes, 8));
    cJSON_AddItemToObject(c, "commandTypes", cJSON_CreateStringArray(commandTypes, 6));
    cJSON_AddItemToObject(c, "clientOperations", cJSON_CreateStringArray(operations, 9));
    emitJson(c);
    return EXIT_OK;
}

static int printFixture(const char* name)
{
    char path[256];
    if (strchr(name, '/') != NULL || strstr(name, "..") != NULL) {
        logf_("print-fixture: '%s' is not a fixture name", name);
        return EXIT_USAGE;
    }
    snprintf(path, sizeof(path), "/fixtures/%s/fixture.json", name);
    FILE* f = fopen(path, "rb");
    if (f == NULL) {
        logf_("print-fixture: no fixture named '%s' in this image", name);
        return EXIT_OPERATION_FAILED;
    }
    char buf[4096];
    size_t n;
    while ((n = fread(buf, 1, sizeof(buf), f)) > 0)
        fwrite(buf, 1, n, stdout);
    fclose(f);
    fflush(stdout);
    return EXIT_OK;
}

int main(int argc, char** argv)
{
    if (argc < 2) {
        fputs(USAGE, stderr);
        return EXIT_USAGE;
    }
    const char* cmd = argv[1];
    if (!strcmp(cmd, "server")) {
        Args a = { argc - 2, argv + 2 };
        return runServer(&a);
    }
    if (!strcmp(cmd, "client")) {
        if (argc < 3) {
            fputs(USAGE, stderr);
            return EXIT_USAGE;
        }
        Args a = { argc - 3, argv + 3 };
        return runClient(argv[2], &a);
    }
    if (!strcmp(cmd, "print-capabilities"))
        return printCapabilities();
    if (!strcmp(cmd, "print-fixture"))
        return printFixture(argc > 2 ? argv[2] : "baseline");
    if (!strcmp(cmd, "version")) {
        printf("%s %s (lib60870-C %s)\n", ADAPTER_NAME, ADAPTER_VERSION, UPSTREAM_VERSION);
        return EXIT_OK;
    }
    if (!strcmp(cmd, "help") || !strcmp(cmd, "--help") || !strcmp(cmd, "-h")) {
        fputs(USAGE, stdout);
        return EXIT_OK;
    }
    fputs(USAGE, stderr);
    return EXIT_USAGE;
}
