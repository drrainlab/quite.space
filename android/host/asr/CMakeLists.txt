cmake_minimum_required(VERSION 3.22)
project(quietasr CXX C)

# whisper.cpp PINNED by tag — never floating. FetchContent keeps five
# megabytes of vendored C++ out of the repo while the tag keeps the build
# reproducible; the model artifact is pinned separately by sha256 in
# ModelStore.
include(FetchContent)
FetchContent_Declare(
  whisper
  GIT_REPOSITORY https://github.com/ggml-org/whisper.cpp
  GIT_TAG        b4938
  GIT_SHALLOW    TRUE
)
set(WHISPER_BUILD_TESTS OFF CACHE BOOL "" FORCE)
set(WHISPER_BUILD_EXAMPLES OFF CACHE BOOL "" FORCE)
set(BUILD_SHARED_LIBS OFF CACHE BOOL "" FORCE)
set(GGML_OPENMP OFF CACHE BOOL "" FORCE)
FetchContent_MakeAvailable(whisper)

add_library(quietasr SHARED src/main/cpp/whisper_jni.cpp)
target_link_libraries(quietasr whisper log)
