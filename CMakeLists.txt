cmake_minimum_required(VERSION 3.20)
project(sec_fa LANGUAGES CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

add_library(folio SHARED
    core/core.cpp
    core/growth.cpp
    core/value.cpp
    core/gate.cpp
)
target_include_directories(folio PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_compile_options(folio PRIVATE
    -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Werror
    -fno-exceptions -fno-rtti
)

enable_testing()
add_executable(coretest tests/test_core.cpp)
target_include_directories(coretest PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_link_libraries(coretest PRIVATE folio)
target_compile_options(coretest PRIVATE
    -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Werror
    -fno-exceptions -fno-rtti
)
add_test(NAME coretest COMMAND coretest)

add_executable(valuationtest tests/test_valuation.cpp)
target_include_directories(valuationtest PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_link_libraries(valuationtest PRIVATE folio)
target_compile_options(valuationtest PRIVATE
    -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Werror
    -fno-exceptions -fno-rtti
)
add_test(NAME valuationtest COMMAND valuationtest)
