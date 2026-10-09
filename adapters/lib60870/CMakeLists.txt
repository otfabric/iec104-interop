# SPDX-License-Identifier: GPL-3.0-or-later
cmake_minimum_required(VERSION 3.16)
project(iec104-interop-lib60870 C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

set(LIB60870_PREFIX "/opt/lib60870" CACHE PATH "lib60870-C install prefix")
set(ADAPTER_VERSION "dev" CACHE STRING "adapter version reported by print-capabilities")
set(UPSTREAM_VERSION "unknown" CACHE STRING "lib60870 version")
set(UPSTREAM_REVISION "unknown" CACHE STRING "lib60870 commit")

find_package(PkgConfig REQUIRED)
pkg_check_modules(CJSON REQUIRED libcjson)
find_package(Threads REQUIRED)

add_executable(iec104-interop
  src/main.c src/server.c src/client.c src/fixture.c src/asdu_json.c src/util.c)

target_compile_definitions(iec104-interop PRIVATE
  _GNU_SOURCE
  ADAPTER_VERSION="${ADAPTER_VERSION}"
  UPSTREAM_VERSION="${UPSTREAM_VERSION}"
  UPSTREAM_REVISION="${UPSTREAM_REVISION}")
target_compile_options(iec104-interop PRIVATE -Wall -Wextra -Werror)
target_include_directories(iec104-interop PRIVATE
  ${LIB60870_PREFIX}/include/lib60870 ${CJSON_INCLUDE_DIRS})
target_link_libraries(iec104-interop PRIVATE
  ${LIB60870_PREFIX}/lib/liblib60870.a ${CJSON_LIBRARIES} Threads::Threads m)

install(TARGETS iec104-interop RUNTIME DESTINATION bin)
