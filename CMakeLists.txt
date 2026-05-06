cmake_minimum_required(VERSION 3.20)
project(sec_fa LANGUAGES CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

add_library(fa_core SHARED core/fa_core.cpp)
target_include_directories(fa_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_compile_options(fa_core PRIVATE
    -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Werror
    -fno-exceptions -fno-rtti
)

enable_testing()
add_executable(fa_core_test tests/test_core.cpp)
target_include_directories(fa_core_test PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_link_libraries(fa_core_test PRIVATE fa_core)
target_compile_options(fa_core_test PRIVATE
    -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Werror
    -fno-exceptions -fno-rtti
)
add_test(NAME fa_core_test COMMAND fa_core_test)
