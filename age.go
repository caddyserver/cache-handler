// Copyright 2015 Matthew Holt and The Caddy Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpcache

import (
	"math"
	"strconv"
	"time"
)

// Specification of these calculations: https://httpwg.org/specs/rfc7234.html#age.calculations

// apparent_age = max(0, response_time - date_value);
//
// response_delay = response_time - request_time;
// corrected_age_value = age_value + response_delay;
//
// corrected_initial_age = max(apparent_age, corrected_age_value);
func correctedInitialAge(responseTime, dateValue, requestTime time.Time, ageValue string) time.Duration {
	apparentAge := responseTime.Sub(dateValue)
	if apparentAge < 0 {
		apparentAge = 0
	}

	var initialAge time.Duration
	if ageValue != "" {
		if iAgeValue, _ := strconv.Atoi(ageValue); iAgeValue != 0 {
			initialAge = time.Duration(iAgeValue) * time.Second
		}
	}

	responseDelay := responseTime.Sub(requestTime)
	correctedAgeValue := initialAge + responseDelay

	if apparentAge > correctedAgeValue {
		return apparentAge
	}
	return correctedAgeValue
}

// resident_time = now - response_time;
// current_age = corrected_initial_age + resident_time;
func currentAge(responseTime time.Time, correctedInitialAge time.Duration) string {
	return ageToString(correctedInitialAge + time.Now().UTC().Sub(responseTime))
}

func ageToString(age time.Duration) string {
	return strconv.Itoa(int(math.Ceil(age.Seconds())))
}
